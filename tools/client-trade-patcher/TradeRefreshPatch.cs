using Mono.Cecil;
using Mono.Cecil.Cil;

internal static class TradeRefreshPatch
{
    public static void InstallSignal(ModuleDefinition module, TypeDefinition teamHelper)
    {
        var version = teamHelper.Fields.SingleOrDefault(field => field.Name == "CodexTradeRefreshVersion")
            ?? new FieldDefinition("CodexTradeRefreshVersion",
                FieldAttributes.Public | FieldAttributes.Static, module.TypeSystem.Int64);
        if (version.DeclaringType == null)
            teamHelper.Fields.Add(version);

        var handler = module.Types.SelectMany(AllTypes)
            .Single(type => type.FullName == "ET.M2C_SendTipHandler");
        var run = handler.Methods.Single(method => method.Name == "Run");
        if (run.Body.Instructions.Any(instruction => instruction.Operand is FieldReference field
            && field.Name == version.Name))
            return;
        var first = run.Body.Instructions[0];
        var il = run.Body.GetILProcessor();
        il.InsertBefore(first, Instruction.Create(OpCodes.Ldsfld, version));
        il.InsertBefore(first, Instruction.Create(OpCodes.Ldc_I4_1));
        il.InsertBefore(first, Instruction.Create(OpCodes.Conv_I8));
        il.InsertBefore(first, Instruction.Create(OpCodes.Add));
        il.InsertBefore(first, Instruction.Create(OpCodes.Stsfld, version));
    }

    public static void BuildUpdateSystem(ModuleDefinition module, TypeDefinition storeUI,
        FieldDefinition mode, FieldDefinition lastVersion, FieldDefinition refreshPending)
    {
        if (module.Types.Any(type => type.FullName == "ET.StoreUITradeUpdateSystem"))
            return;
        var template = module.Types.First(type => type.BaseType is GenericInstanceType generic
            && generic.ElementType.FullName == "ET.UpdateSystem`1");
        var baseType = new GenericInstanceType(module.ImportReference(
            ((GenericInstanceType)template.BaseType).ElementType));
        baseType.GenericArguments.Add(storeUI);
        var system = new TypeDefinition("ET", "StoreUITradeUpdateSystem",
            TypeAttributes.Public | TypeAttributes.Class | TypeAttributes.BeforeFieldInit, baseType);
        module.Types.Add(system);

        var ctor = new MethodDefinition(".ctor",
            MethodAttributes.Public | MethodAttributes.HideBySig |
            MethodAttributes.SpecialName | MethodAttributes.RTSpecialName,
            module.TypeSystem.Void);
        system.Methods.Add(ctor);
        var baseCtor = new MethodReference(".ctor", module.TypeSystem.Void, baseType) { HasThis = true };
        var ctorIL = ctor.Body.GetILProcessor();
        ctorIL.Append(Instruction.Create(OpCodes.Ldarg_0));
        ctorIL.Append(Instruction.Create(OpCodes.Call, baseCtor));
        ctorIL.Append(Instruction.Create(OpCodes.Ret));

        var update = new MethodDefinition("Update",
            MethodAttributes.Public | MethodAttributes.Virtual | MethodAttributes.HideBySig,
            module.TypeSystem.Void);
        update.Parameters.Add(new ParameterDefinition("self", ParameterAttributes.None, storeUI));
        update.Body.Variables.Add(new VariableDefinition(module.TypeSystem.Int64));
        system.Methods.Add(update);

        var getStore = storeUI.Methods.Single(method => method.Name == "GetStoreSlot");
        getStore.Attributes = (getStore.Attributes & ~MethodAttributes.MemberAccessMask) |
            MethodAttributes.Public;
        var configure = storeUI.Methods.Single(method => method.Name == "Codex_ConfigureTradeUI");
        var ui = storeUI.Fields.Single(field => field.Name == "ui");
        var coroutine = module.GetMemberReferences().OfType<MethodReference>().First(method =>
            method.DeclaringType.FullName == "ET.ETTask" && method.Name == "Coroutine" &&
            method.Parameters.Count == 0);
        var coroutineTask = new VariableDefinition(module.ImportReference(coroutine.DeclaringType));
        update.Body.Variables.Add(coroutineTask);
        var teamHelper = module.GetTypeReferences().First(type => type.FullName == "ET.TeamHelper");
        var signal = new FieldReference("CodexTradeRefreshVersion", module.TypeSystem.Int64,
            module.ImportReference(teamHelper));

        var done = Instruction.Create(OpCodes.Ret);
        var il = update.Body.GetILProcessor();
        il.Append(Instruction.Create(OpCodes.Ldsfld, mode));
        il.Append(Instruction.Create(OpCodes.Brfalse, done));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Call, configure));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Ldfld, refreshPending));
        il.Append(Instruction.Create(OpCodes.Brtrue, done));
        il.Append(Instruction.Create(OpCodes.Ldsfld, signal));
        il.Append(Instruction.Create(OpCodes.Stloc_0));
        il.Append(Instruction.Create(OpCodes.Ldloc_0));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Ldfld, lastVersion));
        il.Append(Instruction.Create(OpCodes.Beq, done));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Ldloc_0));
        il.Append(Instruction.Create(OpCodes.Stfld, lastVersion));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
        il.Append(Instruction.Create(OpCodes.Stfld, refreshPending));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Ldfld, ui));
        il.Append(Instruction.Create(OpCodes.Call, getStore));
        il.Append(Instruction.Create(OpCodes.Stloc, coroutineTask));
        il.Append(Instruction.Create(OpCodes.Ldloca_S, coroutineTask));
        il.Append(Instruction.Create(OpCodes.Call, coroutine));
        il.Append(done);
    }

    private static IEnumerable<TypeDefinition> AllTypes(TypeDefinition type)
    {
        yield return type;
        foreach (var nested in type.NestedTypes.SelectMany(AllTypes))
            yield return nested;
    }
}
