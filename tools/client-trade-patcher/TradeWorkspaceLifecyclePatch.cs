using Mono.Cecil;
using Mono.Cecil.Cil;

internal static class TradeWorkspaceLifecyclePatch
{
    private const string StoreUiName = "ui://Bag/StoreUI";

    public static void InstallHotfixSignal(ModuleDefinition module)
    {
        var teamHelper = FindType(module, "ET.TeamHelper");
        var signal = teamHelper.Fields.Single(field => field.Name == "CodexTradeMode");
        var handler = FindType(module, "ET.M2C_OpenStoreUIHandler");
        var state = handler.NestedTypes.Single(type => type.Name == "<Run>d__0");
        var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
        var publish = moveNext.Body.Instructions.Single(instruction =>
            instruction.OpCode == OpCodes.Callvirt &&
            instruction.Operand is GenericInstanceMethod method &&
            method.Name == "Publish" &&
            method.GenericArguments.Any(argument => argument.FullName == "ET.EventType.OpenStoreUI"));

        if (publish.Previous?.OpCode == OpCodes.Stsfld &&
            publish.Previous.Operand is FieldReference existing && existing.Name == signal.Name)
            return;

        var il = moveNext.Body.GetILProcessor();
        il.InsertBefore(publish, Instruction.Create(OpCodes.Ldc_I4_1));
        il.InsertBefore(publish, Instruction.Create(OpCodes.Stsfld, signal));
    }

    public static void InstallViewLifecycle(ModuleDefinition module)
    {
        var storeUI = FindType(module, "ET.StoreUI");
        var mode = storeUI.Fields.Single(field => field.Name == "CodexTradeMode");
        var configured = storeUI.Fields.Single(field => field.Name == "CodexTradeConfigured");
        var dirty = storeUI.Fields.SingleOrDefault(field => field.Name == "CodexTradeDirty")
            ?? new FieldDefinition("CodexTradeDirty", FieldAttributes.Public | FieldAttributes.Static,
                module.TypeSystem.Boolean);
        if (dirty.DeclaringType == null)
            storeUI.Fields.Add(dirty);
        dirty.Attributes = (dirty.Attributes & ~FieldAttributes.FieldAccessMask) | FieldAttributes.Public;

        PatchTradeConfigurationMarker(storeUI, configured, dirty);
        PatchOpenStoreMode(module, mode, configured, dirty);
        VerifyLifecycle(module, mode, configured, dirty);
    }

    private static void PatchTradeConfigurationMarker(TypeDefinition storeUI, FieldDefinition configured,
        FieldDefinition dirty)
    {
        var configure = storeUI.Methods.Single(method => method.Name == "Codex_ConfigureTradeUI");
        var configuredStore = configure.Body.Instructions.Single(instruction =>
            instruction.OpCode == OpCodes.Stsfld &&
            instruction.Operand is FieldReference field && field.Name == configured.Name);
        if (configuredStore.Previous?.OpCode == OpCodes.Stsfld &&
            configuredStore.Previous.Operand is FieldReference existing && existing.Name == dirty.Name)
            return;

        var il = configure.Body.GetILProcessor();
        il.InsertBefore(configuredStore, Instruction.Create(OpCodes.Ldc_I4_1));
        il.InsertBefore(configuredStore, Instruction.Create(OpCodes.Stsfld, dirty));
    }

    private static void PatchOpenStoreMode(ModuleDefinition module, FieldDefinition mode,
        FieldDefinition configured, FieldDefinition dirty)
    {
        var openStore = FindType(module, "ET.OpenStoreEvent");
        var state = openStore.NestedTypes.Single(type => type.Name == "<Run>d__0");
        var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
        var instructions = moveNext.Body.Instructions;
        var modeStore = instructions.First(instruction =>
            instruction.OpCode == OpCodes.Stsfld &&
            instruction.Operand is FieldReference field && field.Name == mode.Name);
        var oldModeRead = modeStore.Previous;
        var oldArgsAddress = oldModeRead?.Previous;
        var oldStateLoad = oldArgsAddress?.Previous;
        if (oldModeRead == null || oldArgsAddress == null || oldStateLoad == null ||
            oldModeRead.OpCode != OpCodes.Ldfld ||
            oldModeRead.Operand is not FieldReference oldField || oldField.Name != "ignoreInfo" ||
            oldArgsAddress.OpCode != OpCodes.Ldflda || oldStateLoad.OpCode != OpCodes.Ldarg_0)
            throw new InvalidOperationException("OpenStoreEvent trade mode assignment was not structurally recognized");

        var teamHelper = module.GetTypeReferences().First(type => type.FullName == "ET.TeamHelper");
        var signal = new FieldReference("CodexTradeMode", module.TypeSystem.Boolean,
            module.ImportReference(teamHelper));

        var il = moveNext.Body.GetILProcessor();
        InsertDirtyStoreRemoval(module, il, oldStateLoad, configured, dirty);

        oldStateLoad.OpCode = OpCodes.Ldsfld;
        oldStateLoad.Operand = signal;
        oldArgsAddress.OpCode = OpCodes.Stsfld;
        oldArgsAddress.Operand = mode;
        oldModeRead.OpCode = OpCodes.Ldc_I4_0;
        oldModeRead.Operand = null;
        modeStore.OpCode = OpCodes.Stsfld;
        modeStore.Operand = signal;
    }

    private static void InsertDirtyStoreRemoval(ModuleDefinition module, ILProcessor il, Instruction continueOpen,
        FieldDefinition configured, FieldDefinition dirty)
    {
        if (continueOpen.Previous?.OpCode == OpCodes.Stsfld &&
            continueOpen.Previous.Operand is FieldReference existing && existing.Name == configured.Name)
            return;

        var fuiComponent = FindType(module, "ET.FUIComponent");
        var getInstance = fuiComponent.Methods.Single(method => method.Name == "get_Instance");
        var remove = fuiComponent.Methods.Single(method => method.Name == "Remove" &&
            method.Parameters.Count == 1 && method.Parameters[0].ParameterType.MetadataType == MetadataType.String);
        foreach (var instruction in new[]
        {
            Instruction.Create(OpCodes.Ldsfld, dirty),
            Instruction.Create(OpCodes.Brfalse, continueOpen),
            Instruction.Create(OpCodes.Call, getInstance),
            Instruction.Create(OpCodes.Ldstr, StoreUiName),
            Instruction.Create(OpCodes.Callvirt, remove),
            Instruction.Create(OpCodes.Ldc_I4_0),
            Instruction.Create(OpCodes.Stsfld, dirty),
            Instruction.Create(OpCodes.Ldc_I4_0),
            Instruction.Create(OpCodes.Stsfld, configured),
        })
            il.InsertBefore(continueOpen, instruction);
    }

    private static void VerifyLifecycle(ModuleDefinition module, FieldDefinition mode,
        FieldDefinition configured, FieldDefinition dirty)
    {
        if (!dirty.IsPublic || !dirty.IsStatic)
            throw new InvalidOperationException("CodexTradeDirty must be public static for OpenStoreEvent");
        var openStore = FindType(module, "ET.OpenStoreEvent");
        var moveNext = openStore.NestedTypes.Single(type => type.Name == "<Run>d__0")
            .Methods.Single(method => method.Name == "MoveNext");
        if (moveNext.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Stsfld &&
            instruction.Operand is FieldReference field && field.Name == mode.Name &&
            instruction.Previous?.OpCode == OpCodes.Ldfld &&
            instruction.Previous.Operand is FieldReference source && source.Name == "ignoreInfo"))
            throw new InvalidOperationException("OpenStoreEvent still aliases ignoreInfo to trade mode");
        foreach (var field in new[] { configured, dirty })
        {
            if (!moveNext.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Stsfld &&
                instruction.Operand is FieldReference stored && stored.Name == field.Name))
                throw new InvalidOperationException($"OpenStoreEvent does not reset {field.Name}");
        }

        var teamHelper = module.GetTypeReferences().First(type => type.FullName == "ET.TeamHelper");
        if (!moveNext.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Ldsfld &&
            instruction.Operand is FieldReference field && field.Name == "CodexTradeMode" &&
            field.DeclaringType.FullName == teamHelper.FullName))
            throw new InvalidOperationException("OpenStoreEvent does not consume the explicit trade signal");
    }

    private static TypeDefinition FindType(ModuleDefinition module, string name) =>
        module.Types.SelectMany(AllTypes).Single(type => type.FullName == name);

    private static IEnumerable<TypeDefinition> AllTypes(TypeDefinition type)
    {
        yield return type;
        foreach (var nested in type.NestedTypes.SelectMany(AllTypes))
            yield return nested;
    }
}
