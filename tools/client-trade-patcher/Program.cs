using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length == 3 && args[0] == "--repair-store-takeout")
{
    RepairStoreTakeout(args[1], args[2]);
    return 0;
}

if (args.Length == 3 && args[0] == "--repair-request-ui")
{
    RepairRequestWindow(args[1], args[2]);
    return 0;
}

if (args.Length != 3)
{
    Console.Error.WriteLine("usage: ClientTradePatcher <Hotfix.dll> <HotfixView.dll> <output-directory>");
    Console.Error.WriteLine("   or: ClientTradePatcher --repair-store-takeout <HotfixView.dll> <output.dll>");
    Console.Error.WriteLine("   or: ClientTradePatcher --repair-request-ui <HotfixView.dll> <output.dll>");
    return 2;
}

var hotfixPath = Path.GetFullPath(args[0]);
var viewPath = Path.GetFullPath(args[1]);
var output = Path.GetFullPath(args[2]);
Directory.CreateDirectory(output);
var resolver = new DefaultAssemblyResolver();
resolver.AddSearchDirectory(Path.GetDirectoryName(hotfixPath)!);
resolver.AddSearchDirectory(Path.GetDirectoryName(viewPath)!);
var managedDirectory = Path.GetFullPath(Path.Combine(Path.GetDirectoryName(viewPath)!, "..", "..", "..", "Managed"));
if (Directory.Exists(managedDirectory))
    resolver.AddSearchDirectory(managedDirectory);

using (var module = ModuleDefinition.ReadModule(hotfixPath, new ReaderParameters { InMemory = true, ReadSymbols = false, AssemblyResolver = resolver }))
{
    PatchHotfix(module);
    module.Write(Path.Combine(output, "Hotfix.dll"), new WriterParameters { WriteSymbols = false });
}
using (var module = ModuleDefinition.ReadModule(viewPath, new ReaderParameters { InMemory = true, ReadSymbols = false, AssemblyResolver = resolver }))
{
    PatchHotfixView(module);
    module.Write(Path.Combine(output, "HotfixView.dll"), new WriterParameters { WriteSymbols = false });
}

Console.WriteLine("交易菜单 -> TeamHelper.RequestTrade（负 TargetId 标记）");
Console.WriteLine("交易申请 -> 独立列表项，正 ID 的组队申请不变");
return 0;

static void PatchHotfix(ModuleDefinition module)
{
    AddTradeSnapshotProperties(module);
    var helper = FindType(module, "ET.TeamHelper");
    if (helper.Fields.All(f => f.Name != "CodexTradeMode"))
        helper.Fields.Add(new FieldDefinition("CodexTradeMode", FieldAttributes.Public | FieldAttributes.Static, module.TypeSystem.Boolean));
    var mode = helper.Fields.Single(f => f.Name == "CodexTradeMode");
    TradeRefreshPatch.InstallSignal(module, helper);
    var requestMethod = helper.Methods.Single(m => m.Name == "RequestTeam");
    var method = helper.Methods.SingleOrDefault(m => m.Name == "RequestTrade");
    if (method == null)
    {
        method = new MethodDefinition("RequestTrade", MethodAttributes.Public | MethodAttributes.Static | MethodAttributes.HideBySig,
            module.ImportReference(requestMethod.ReturnType));
        method.Parameters.Add(new ParameterDefinition("zoneScene", ParameterAttributes.None, module.ImportReference(requestMethod.Parameters[0].ParameterType)));
        helper.Methods.Add(method);
    }
    method.ReturnType = module.ImportReference(requestMethod.ReturnType);
    method.Parameters[0].ParameterType = module.ImportReference(requestMethod.Parameters[0].ParameterType);
    var il = method.Body.GetILProcessor();
    il.Clear();
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Stsfld, mode));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Call, requestMethod));
    il.Append(Instruction.Create(OpCodes.Ret));

    var state = requestMethod.DeclaringType.NestedTypes.Single(t => t.Name == "<RequestTeam>d__1");
    var moveNext = state.Methods.Single(m => m.Name == "MoveNext");
    if (!moveNext.Body.Instructions.Any(i => i.OpCode == OpCodes.Ldsfld && i.Operand is FieldReference f && f.Name == mode.Name))
    {
        var setter = moveNext.Body.Instructions.Single(i => i.OpCode == OpCodes.Callvirt && i.Operand is MethodReference m && m.Name == "set_TargetId");
        var ilState = moveNext.Body.GetILProcessor();
        var afterNeg = Instruction.Create(OpCodes.Nop);
        var skip = Instruction.Create(OpCodes.Brfalse, afterNeg);
        foreach (var instruction in new[]
        {
            Instruction.Create(OpCodes.Ldsfld, mode),
            skip,
            Instruction.Create(OpCodes.Neg),
            afterNeg,
            Instruction.Create(OpCodes.Ldc_I4_0),
            Instruction.Create(OpCodes.Stsfld, mode),
        })
            ilState.InsertBefore(setter, instruction);
    }
    PatchTradeRequestMarker(module);
    PatchTradeOpenHandler(module);
    TradeWorkspaceLifecyclePatch.InstallHotfixSignal(module);
}

static void PatchManualLoginToPasswordMode(ModuleDefinition module)
{
    var loginSystem = FindType(module, "ET.FUI_LoginStartSystem");
    var start = loginSystem.Methods.Single(method => method.Name == "Start");
    var loginTypeStart = start.Body.Instructions.FirstOrDefault(instruction => instruction.OpCode == OpCodes.Stfld
        && instruction.Operand is FieldReference field && field.Name == "loginType");
    if (loginTypeStart == null)
        throw new InvalidOperationException("login start method has no loginType field");
    var loginTypeStartField = (FieldReference)loginTypeStart.Operand;

    // A cached LoginVoucher is only for LoginHelper.ReLogin. The visible login
    // form must never put that one-time key into the password input.
    var voucherPasswordStores = start.Body.Instructions.Where(instruction => instruction.OpCode == OpCodes.Callvirt
        && instruction.Operand is MethodReference method && method.Name == "set_text")
        .ToList();
    var passwordField = FindType(module, "ET.FUI_Login").Fields.Single(field => field.Name == "m_iptPsd");
    var passwordStore = voucherPasswordStores.FirstOrDefault(instruction =>
        start.Body.Instructions.IndexOf(instruction) > start.Body.Instructions.IndexOf(loginTypeStart) &&
        start.Body.Instructions.Take(start.Body.Instructions.IndexOf(instruction)).Any(previous =>
            previous.OpCode == OpCodes.Ldfld && previous.Operand is FieldReference field && field.Name == passwordField.Name));
    if (passwordStore != null)
    {
        var ilStart = start.Body.GetILProcessor();
        var beforeStore = passwordStore;
        // Replace the voucher value with an empty password while retaining the
        // original UI field receiver and setter call.
        var previous = beforeStore.Previous;
        while (previous != null && previous.OpCode != OpCodes.Ldfld)
            previous = previous.Previous;
        if (previous != null && previous.Operand is FieldReference field && field.Name == passwordField.Name)
        {
            var valueStart = previous.Next;
            var cursor = valueStart;
            while (cursor != null && cursor != beforeStore)
            {
                var next = cursor.Next;
                ilStart.Remove(cursor);
                cursor = next;
            }
            ilStart.InsertBefore(beforeStore, Instruction.Create(OpCodes.Ldstr, string.Empty));
        }
    }

    // Clear the cached Voucher mode after the optional account prefill. This
    // affects only the manual button handler; ReLogin keeps LoginType=Voucher.
    var il = start.Body.GetILProcessor();
    foreach (var instruction in start.Body.Instructions.Where(instruction => instruction.OpCode == OpCodes.Ret).ToList())
    {
        il.InsertBefore(instruction, Instruction.Create(OpCodes.Ldloc_0));
        il.InsertBefore(instruction, Instruction.Create(OpCodes.Ldc_I4_0));
        il.InsertBefore(instruction, Instruction.Create(OpCodes.Stfld, loginTypeStartField));
    }

    var closure = loginSystem.NestedTypes.Single(type => type.Name == "<>c__DisplayClass0_0");
    var callback = closure.Methods.Single(method => method.Name == "<Start>b__0");
    var loginType = closure.Fields.Single(field => field.Name == "loginType");

    var loginCall = callback.Body.Instructions.FirstOrDefault(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method
        && method.Name == "OnLoginAsync"
        && method.DeclaringType.FullName == "ET.LoginHelper");
    if (loginCall == null)
        throw new InvalidOperationException("manual login callback was not structurally recognized");

    if (!callback.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Stfld
        && instruction.Operand is FieldReference field && field.Name == loginType.Name))
    {
        var callbackIL = callback.Body.GetILProcessor();
        callbackIL.InsertBefore(loginCall, Instruction.Create(OpCodes.Ldarg_0));
        callbackIL.InsertBefore(loginCall, Instruction.Create(OpCodes.Ldc_I4_0));
        callbackIL.InsertBefore(loginCall, Instruction.Create(OpCodes.Stfld, loginType));
    }
}

static void AddTradeSnapshotProperties(ModuleDefinition module)
{
    var message = FindType(module, "ET.M2C_GetStore");
    var templateProperty = message.Properties.Single(property => property.Name == "Coin");
    var templateField = message.Fields.Single(field => field.Name == "<Coin>k__BackingField");
    foreach (var (name, type, tag) in new[]
    {
        ("SelfCoin", module.TypeSystem.Int64, 4),
        ("OtherCoin", module.TypeSystem.Int64, 5),
        ("SelfLocked", module.TypeSystem.Boolean, 6),
        ("OtherLocked", module.TypeSystem.Boolean, 7),
        ("SelfConfirmed", module.TypeSystem.Boolean, 8),
        ("OtherConfirmed", module.TypeSystem.Boolean, 9),
    })
    {
        if (message.Properties.Any(property => property.Name == name))
            continue;
        var backing = new FieldDefinition($"<{name}>k__BackingField", templateField.Attributes, type);
        foreach (var attribute in templateField.CustomAttributes)
            backing.CustomAttributes.Add(CloneAttribute(module, attribute));
        message.Fields.Add(backing);

        var getter = new MethodDefinition($"get_{name}", templateProperty.GetMethod.Attributes, type);
        var getterIL = getter.Body.GetILProcessor();
        getterIL.Append(Instruction.Create(OpCodes.Ldarg_0));
        getterIL.Append(Instruction.Create(OpCodes.Ldfld, backing));
        getterIL.Append(Instruction.Create(OpCodes.Ret));
        message.Methods.Add(getter);

        var setter = new MethodDefinition($"set_{name}", templateProperty.SetMethod.Attributes, module.TypeSystem.Void);
        setter.Parameters.Add(new ParameterDefinition("value", ParameterAttributes.None, type));
        var setterIL = setter.Body.GetILProcessor();
        setterIL.Append(Instruction.Create(OpCodes.Ldarg_0));
        setterIL.Append(Instruction.Create(OpCodes.Ldarg_1));
        setterIL.Append(Instruction.Create(OpCodes.Stfld, backing));
        setterIL.Append(Instruction.Create(OpCodes.Ret));
        message.Methods.Add(setter);

        var property = new PropertyDefinition(name, PropertyAttributes.None, type)
        {
            GetMethod = getter,
            SetMethod = setter,
        };
        var protoMember = CloneAttribute(module, templateProperty.CustomAttributes.Single(attribute =>
            attribute.AttributeType.FullName == "ProtoBuf.ProtoMemberAttribute"));
        protoMember.ConstructorArguments[0] = new CustomAttributeArgument(module.TypeSystem.Int32, tag);
        property.CustomAttributes.Add(protoMember);
        message.Properties.Add(property);
    }
}

static void PatchTradeOpenHandler(ModuleDefinition module)
{
    var handler = FindType(module, "ET.M2C_OpenStoreUIHandler");
    var state = handler.NestedTypes.Single(t => t.Name == "<Run>d__0");
    var moveNext = state.Methods.Single(m => m.Name == "MoveNext");
    if (state.Fields.Any(f => f.Name == "CodexTradeMessage"))
    {
        RemoveLegacyTradeBagOpen(module, moveNext);
        RepairAsyncResumeBranch(moveNext);
        return;
    }

    var messageType = FindType(module, "ET.M2C_OpenStoreUI");
    var messageField = new FieldDefinition("CodexTradeMessage", FieldAttributes.Public, messageType);
    state.Fields.Add(messageField);

    var run = handler.Methods.Single(m => m.Name == "Run");
    var sessionStore = run.Body.Instructions.First(i => i.OpCode == OpCodes.Stfld
        && i.Operand is FieldReference field && field.Name == "session");
    var stateLocal = (VariableDefinition)((Instruction)sessionStore.Previous!.Previous!).Operand;
    var runIL = run.Body.GetILProcessor();
    var cursor = sessionStore;
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldloca_S, stateLocal),
        Instruction.Create(OpCodes.Ldarg_2),
        Instruction.Create(OpCodes.Stfld, messageField),
    })
    {
        runIL.InsertAfter(cursor, instruction);
        cursor = instruction;
    }

    var instructions = moveNext.Body.Instructions;
    var normal = instructions.First(i => i.OpCode == OpCodes.Call
        && i.Operand is MethodReference method && method.DeclaringType.FullName == "ET.Game"
        && method.Name == "get_EventSystem");
    var completed = instructions.First(i => i.OpCode == OpCodes.Call
        && i.Operand is MethodReference method && method.DeclaringType.FullName == "ET.ETTask"
        && method.Name == "get_CompletedTask");
    var publishTemplate = (GenericInstanceMethod)instructions.First(i => i.OpCode == OpCodes.Callvirt
        && i.Operand is GenericInstanceMethod method && method.Name == "Publish").Operand;
    var coroutine = (MethodReference)instructions.First(i => i.OpCode == OpCodes.Call
        && i.Operand is MethodReference method && method.DeclaringType.FullName == "ET.ETTask"
        && method.Name == "Coroutine").Operand;
    var gameEventSystem = (MethodReference)normal.Operand;
    var sessionField = state.Fields.Single(f => f.Name == "session");
    var zoneScene = (MethodReference)instructions.First(i => i.OpCode == OpCodes.Call
        && i.Operand is MethodReference method && method.Name == "ZoneScene").Operand;
    var actorId = messageType.Methods.Single(m => m.Name == "get_ActorId");
    var openStore = FindType(module, "ET.EventType.OpenStoreUI");
    var storeZone = openStore.Fields.Single(f => f.Name == "zoneScene");
    var ignoreInfo = openStore.Fields.Single(f => f.Name == "ignoreInfo");
    var storeLocal = new VariableDefinition(openStore);
    moveNext.Body.Variables.Add(storeLocal);
    var taskLocal = moveNext.Body.Variables.First(v => v.VariableType.FullName == "ET.ETTask");

    var il = moveNext.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldarg_0),
        Instruction.Create(OpCodes.Ldfld, messageField),
        Instruction.Create(OpCodes.Callvirt, actorId),
        Instruction.Create(OpCodes.Ldc_I8, 0L),
        Instruction.Create(OpCodes.Bge, normal),

        Instruction.Create(OpCodes.Call, gameEventSystem),
        Instruction.Create(OpCodes.Ldloca_S, storeLocal),
        Instruction.Create(OpCodes.Initobj, openStore),
        Instruction.Create(OpCodes.Ldloca_S, storeLocal),
        Instruction.Create(OpCodes.Ldarg_0),
        Instruction.Create(OpCodes.Ldfld, sessionField),
        Instruction.Create(OpCodes.Call, zoneScene),
        Instruction.Create(OpCodes.Stfld, storeZone),
        Instruction.Create(OpCodes.Ldloca_S, storeLocal),
        Instruction.Create(OpCodes.Ldc_I4_1),
        Instruction.Create(OpCodes.Stfld, ignoreInfo),
        Instruction.Create(OpCodes.Ldloc, storeLocal),
        Instruction.Create(OpCodes.Callvirt, MakeGenericCall(module, publishTemplate, openStore)),
        Instruction.Create(OpCodes.Stloc, taskLocal),
        Instruction.Create(OpCodes.Ldloca_S, taskLocal),
        Instruction.Create(OpCodes.Call, coroutine),
        Instruction.Create(OpCodes.Br, completed),
    })
        il.InsertBefore(normal, instruction);
    RepairAsyncResumeBranch(moveNext);
}

static void RemoveLegacyTradeBagOpen(ModuleDefinition module, MethodDefinition moveNext)
{
    var instructions = moveNext.Body.Instructions;
    var publishIndex = instructions.Select((instruction, index) => (instruction, index))
        .Where(pair => pair.instruction.OpCode == OpCodes.Callvirt
            && pair.instruction.Operand is GenericInstanceMethod method
            && method.Name == "Publish"
            && method.GenericArguments.Any(argument => argument.FullName == "ET.EventType.OpenBagUI"))
        .Select(pair => pair.index)
        .FirstOrDefault(-1);
    if (publishIndex < 0)
        return;

    var start = instructions.Take(publishIndex).LastOrDefault(instruction =>
        instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method
        && method.DeclaringType.FullName == "ET.Game"
        && method.Name == "get_EventSystem");
    var end = instructions.Skip(publishIndex).FirstOrDefault(instruction =>
        instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method
        && method.DeclaringType.FullName == "ET.ETTask"
        && method.Name == "Coroutine");
    if (start == null || end == null)
        throw new InvalidOperationException("legacy trade bag-open block was not structurally recognized");

    var il = moveNext.Body.GetILProcessor();
    var cursor = start;
    while (true)
    {
        var next = cursor.Next;
        il.Remove(cursor);
        if (cursor == end)
            break;
        cursor = next ?? throw new InvalidOperationException("legacy trade bag-open block ended unexpectedly");
    }
}

static void RepairAsyncResumeBranch(MethodDefinition moveNext)
{
    var instructions = moveNext.Body.Instructions;
    var stateBranch = instructions.First(instruction =>
        instruction.OpCode == OpCodes.Brfalse || instruction.OpCode == OpCodes.Brfalse_S);
    var awaiterRead = instructions.First(instruction => instruction.OpCode == OpCodes.Ldfld
        && instruction.Operand is FieldReference field && field.Name == "<>u__1");
    stateBranch.OpCode = OpCodes.Brfalse;
    stateBranch.Operand = awaiterRead.Previous
        ?? throw new InvalidOperationException("async resume target was not found");
}

static void PatchTradeRequestMarker(ModuleDefinition module)
{
    var handler = FindType(module, "ET.M2C_RequestListHandler");
    var state = handler.NestedTypes.Single(t => t.Name == "<Run>d__0");
    var moveNext = state.Methods.Single(m => m.Name == "MoveNext");
    var infoId = FindType(module, "ET.TeamRequestInfo").Fields.Single(f => f.Name == "Id");
    var idStores = moveNext.Body.Instructions.Where(i => i.OpCode == OpCodes.Stfld && i.Operand is FieldReference f && f.Name == "Id").ToList();
    if (idStores.Count > 1) return;
    var idStore = idStores.Single();
    var infoAddress = moveNext.Body.Instructions.Take(moveNext.Body.Instructions.IndexOf(idStore)).Last(i => i.OpCode == OpCodes.Ldloca_S);
    var infoLocal = (VariableDefinition)infoAddress.Operand;
    var messageField = state.Fields.Single(f => f.Name == "message");
    var getActorId = FindType(module, "ET.M2C_RequestList").Methods.Single(m => m.Name == "get_ActorId");
    var normal = idStore.Next!;
    var il = moveNext.Body.GetILProcessor();
    var additions = new[]
    {
        Instruction.Create(OpCodes.Ldarg_0), Instruction.Create(OpCodes.Ldfld, messageField),
        Instruction.Create(OpCodes.Callvirt, getActorId), Instruction.Create(OpCodes.Ldc_I8, 0L),
        Instruction.Create(OpCodes.Bge, normal), Instruction.Create(OpCodes.Ldloca_S, infoLocal),
        Instruction.Create(OpCodes.Ldarg_0), Instruction.Create(OpCodes.Ldfld, messageField),
        Instruction.Create(OpCodes.Callvirt, getActorId), Instruction.Create(OpCodes.Stfld, infoId),
    };
    var cursor = idStore;
    foreach (var instruction in additions) { il.InsertAfter(cursor, instruction); cursor = instruction; }
}

static void PatchHotfixView(ModuleDefinition module)
{
    PatchManualLoginToPasswordMode(module);
    var click = FindType(module, "ET.ClickOtherPlayerEvent");
    var closure = click.NestedTypes.Single(t => t.Name == "<>c__DisplayClass1_0");
    var normal = closure.Methods.Single(m => m.Name == "<Run>b__4");
    var trade = closure.Methods.SingleOrDefault(m => m.Name == "<Run>b__Trade") ?? CloneMethod(module, normal, "<Run>b__Trade");
    foreach (var instruction in trade.Body.Instructions)
    {
        if (instruction.Operand is MethodReference reference && reference.DeclaringType.FullName == "ET.TeamHelper" && reference.Name == "RequestTeam")
        {
            var replacement = new MethodReference("RequestTrade", module.ImportReference(reference.ReturnType), module.GetTypeReferences().First(t => t.FullName == "ET.TeamHelper"))
            {
                HasThis = reference.HasThis,
            };
            foreach (var parameter in reference.Parameters)
                replacement.Parameters.Add(new ParameterDefinition(parameter.Name, parameter.Attributes, module.ImportReference(parameter.ParameterType)));
            instruction.Operand = replacement;
        }
    }
    var state = click.NestedTypes.Single(t => t.Name == "<Run>d__1");
    var moveNext = state.Methods.Single(m => m.Name == "MoveNext");
    var text = moveNext.Body.Instructions.Single(i => i.OpCode == OpCodes.Ldstr && Equals(i.Operand, "交易"));
    var index = moveNext.Body.Instructions.IndexOf(text);
    var addItem = moveNext.Body.Instructions.Skip(index).Take(30).First(i => i.OpCode == OpCodes.Callvirt
        && i.Operand is MethodReference method && method.DeclaringType.FullName == "FairyGUI.PopupMenu"
        && method.Name == "AddItem");
    var callbackCtor = moveNext.Body.Instructions.Take(moveNext.Body.Instructions.IndexOf(addItem))
        .Last(i => i.OpCode == OpCodes.Newobj && i.Operand is MethodReference method
            && method.DeclaringType.FullName == "FairyGUI.EventCallback1");
    var callbackCtorMethod = (MethodReference)callbackCtor.Operand;
    var callbackStart = text.Next!;
    var callbackEnd = addItem.Previous!;
    var cursor = callbackStart;
    while (true)
    {
        var next = cursor.Next;
        moveNext.Body.GetILProcessor().Remove(cursor);
        if (cursor == callbackEnd) break;
        cursor = next!;
    }
    var il = moveNext.Body.GetILProcessor();
    il.InsertBefore(addItem, Instruction.Create(OpCodes.Ldloc_1));
    il.InsertBefore(addItem, Instruction.Create(OpCodes.Ldftn, trade));
    il.InsertBefore(addItem, Instruction.Create(OpCodes.Newobj, callbackCtorMethod));
    if (!normal.Body.Instructions.Any(i => i.Operand is MethodReference m && m.Name == "RequestTeam"))
        throw new InvalidOperationException("original team request callback was overwritten");
    if (!trade.Body.Instructions.Any(i => i.Operand is MethodReference m && m.Name == "RequestTrade"))
        throw new InvalidOperationException("trade callback was not rebound");
    PatchTradeRequestTitle(module);
    PatchRequestWindowComponentReuse(module);
    PatchTradeStoreUI(module);
    TradeWorkspaceLifecyclePatch.InstallViewLifecycle(module);
}

static void PatchRequestWindowComponentReuse(ModuleDefinition module)
{
    var createEvent = FindType(module, "ET.CreateRequstLabalEvent");
    var state = createEvent.NestedTypes.Single(type => type.Name == "<Run>d__0");
    var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
    var add = moveNext.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Callvirt &&
        instruction.Operand is GenericInstanceMethod method &&
        method.Name == "AddComponent" &&
        method.GenericArguments.Count == 2 &&
        method.GenericArguments[0].FullName == "ET.TeamRequestUI");
    var receiver = add.Previous?.Previous?.Previous;
    if (receiver == null)
        throw new InvalidOperationException("request window AddComponent receiver was not found");
    var existingRemove = receiver.Previous;
    if (existingRemove?.OpCode == OpCodes.Callvirt &&
        existingRemove.Operand is GenericInstanceMethod existingMethod &&
        existingMethod.Name == "RemoveComponent" &&
        existingMethod.GenericArguments.Count == 1 &&
        existingMethod.GenericArguments[0].FullName == "ET.TeamRequestUI")
        return;

    var remove = module.Types.SelectMany(AllTypes).SelectMany(type => type.Methods)
        .Where(method => method.HasBody)
        .SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand)
        .OfType<GenericInstanceMethod>()
        .FirstOrDefault(method => method.Name == "RemoveComponent" &&
            method.GenericArguments.Count == 1 &&
            method.GenericArguments[0].FullName == "ET.TeamRequestUI");
    if (remove == null)
        throw new InvalidOperationException("ET.Entity.RemoveComponent<TeamRequestUI> reference was not found");

    // The request window entity is reused.  A previous invite can leave its
    // TeamRequestUI component attached until the hide callback runs; adding
    // the next invite then throws "entity already has component" and drops
    // the invitation.  Dup keeps the receiver for AddComponent while the
    // duplicate is consumed by RemoveComponent.
    var il = moveNext.Body.GetILProcessor();
    il.InsertBefore(receiver, Instruction.Create(OpCodes.Dup));
    il.InsertBefore(receiver, Instruction.Create(OpCodes.Callvirt, module.ImportReference(remove)));
}

static void PatchTradeStoreUI(ModuleDefinition module)
{
    var storeUI = FindType(module, "ET.StoreUI");
    var mode = storeUI.Fields.SingleOrDefault(f => f.Name == "CodexTradeMode");
    if (mode != null)
    {
        PatchTwoSidedTradeStore(module, storeUI, mode);
        return;
    }
    mode = new FieldDefinition("CodexTradeMode", FieldAttributes.Public | FieldAttributes.Static, module.TypeSystem.Boolean);
    storeUI.Fields.Add(mode);
    storeUI.Fields.Add(new FieldDefinition("CodexTradeConfigured",
        FieldAttributes.Public | FieldAttributes.Static, module.TypeSystem.Boolean));

    var openStore = FindType(module, "ET.OpenStoreEvent");
    var openState = openStore.NestedTypes.Single(t => t.Name == "<Run>d__0");
    var openMove = openState.Methods.Single(m => m.Name == "MoveNext");
    var argsField = openState.Fields.Single(f => f.Name == "args");
    var ignoreRead = openMove.Body.Instructions.First(i => i.OpCode == OpCodes.Ldfld
        && i.Operand is FieldReference field && field.Name == "ignoreInfo");
    var ignoreInfo = (FieldReference)ignoreRead.Operand;
    var openIL = openMove.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldarg_0),
        Instruction.Create(OpCodes.Ldflda, argsField),
        Instruction.Create(OpCodes.Ldfld, ignoreInfo),
        Instruction.Create(OpCodes.Stsfld, mode),
    })
        openIL.InsertBefore(ignoreRead.Previous!.Previous!, instruction);

    var expand = storeUI.Methods.Single(m => m.Name == "<AwakeAsync>b__7_6");
    var sendCancel = storeUI.Methods.Single(m => m.Name == "<AwakeAsync>b__7_16");
    var cancelOrExpand = new MethodDefinition("Codex_CancelOrExpand",
        MethodAttributes.Private | MethodAttributes.HideBySig, module.TypeSystem.Void);
    storeUI.Methods.Add(cancelOrExpand);
    var cancelIL = cancelOrExpand.Body.GetILProcessor();
    var normalExpand = Instruction.Create(OpCodes.Ldarg_0);
    cancelIL.Append(Instruction.Create(OpCodes.Ldsfld, mode));
    cancelIL.Append(Instruction.Create(OpCodes.Brfalse, normalExpand));
    cancelIL.Append(Instruction.Create(OpCodes.Ldarg_0));
    cancelIL.Append(Instruction.Create(OpCodes.Call, sendCancel));
    cancelIL.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    cancelIL.Append(Instruction.Create(OpCodes.Stsfld, mode));
    cancelIL.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    cancelIL.Append(Instruction.Create(OpCodes.Stsfld, storeUI.Fields.Single(f => f.Name == "CodexTradeConfigured")));
    cancelIL.Append(Instruction.Create(OpCodes.Ldarg_0));
    cancelIL.Append(Instruction.Create(OpCodes.Ldfld, storeUI.Fields.Single(f => f.Name == "ui")));
    var getWindow = module.Types.SelectMany(AllTypes).SelectMany(type => type.Methods)
        .Where(method => method.HasBody).SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand).OfType<GenericInstanceMethod>()
        .First(method => method.Name == "GetComponent" && method.GenericArguments.Count == 1
            && method.GenericArguments[0].FullName == "ET.FUIWindowComponent");
    var windowField = module.Types.SelectMany(AllTypes).SelectMany(type => type.Methods)
        .Where(method => method.HasBody).SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand).OfType<FieldReference>()
        .First(field => field.Name == "Window");
    cancelIL.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getWindow)));
    cancelIL.Append(Instruction.Create(OpCodes.Ldfld, module.ImportReference(windowField)));
    cancelIL.Append(Instruction.Create(OpCodes.Callvirt, FindMethodReference(module, "FairyGUI.Window", "Hide")));
    cancelIL.Append(Instruction.Create(OpCodes.Ret));
    cancelIL.Append(normalExpand);
    cancelIL.Append(Instruction.Create(OpCodes.Call, expand));
    cancelIL.Append(Instruction.Create(OpCodes.Ret));

    var closeTrade = new MethodDefinition("Codex_CloseTrade",
        MethodAttributes.Private | MethodAttributes.HideBySig, module.TypeSystem.Void);
    storeUI.Methods.Add(closeTrade);
    var closeIL = closeTrade.Body.GetILProcessor();
    var closeDone = Instruction.Create(OpCodes.Ret);
    closeIL.Append(Instruction.Create(OpCodes.Ldsfld, mode));
    closeIL.Append(Instruction.Create(OpCodes.Brfalse, closeDone));
    closeIL.Append(Instruction.Create(OpCodes.Ldarg_0));
    closeIL.Append(Instruction.Create(OpCodes.Call, sendCancel));
    closeIL.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    closeIL.Append(Instruction.Create(OpCodes.Stsfld, mode));
    closeIL.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    closeIL.Append(Instruction.Create(OpCodes.Stsfld, storeUI.Fields.Single(f => f.Name == "CodexTradeConfigured")));
    closeIL.Append(closeDone);

    var configure = new MethodDefinition("Codex_ConfigureTradeUI",
        MethodAttributes.Private | MethodAttributes.HideBySig, module.TypeSystem.Void);
    storeUI.Methods.Add(configure);
    BuildTradeUIConfigurator(module, storeUI, mode, closeTrade, configure);

    var awake = storeUI.Methods.Single(m => m.Name == "Awake");
    var awakeRet = awake.Body.Instructions.Last(i => i.OpCode == OpCodes.Ret);
    var awakeIL = awake.Body.GetILProcessor();
    awakeIL.InsertBefore(awakeRet, Instruction.Create(OpCodes.Ldarg_0));
    awakeIL.InsertBefore(awakeRet, Instruction.Create(OpCodes.Call, configure));

    var awakeState = storeUI.NestedTypes.Single(t => t.Name == "<AwakeAsync>d__7");
    var awakeMove = awakeState.Methods.Single(m => m.Name == "MoveNext");
    var extandField = FindType(module, "ET.FUI_StoreUI").Fields.Single(f => f.Name == "m_btnExtand");
    var extandRead = awakeMove.Body.Instructions.First(i => i.OpCode == OpCodes.Ldfld
        && i.Operand is FieldReference field && field.Name == extandField.Name);
    var delegateTarget = awakeMove.Body.Instructions.Skip(awakeMove.Body.Instructions.IndexOf(extandRead))
        .Take(12).First(i => i.OpCode == OpCodes.Ldftn);
    delegateTarget.Operand = cancelOrExpand;

    foreach (var instruction in AllTypes(storeUI).SelectMany(type => type.Methods)
        .Where(method => method.HasBody).SelectMany(method => method.Body.Instructions))
    {
        if (instruction.OpCode != OpCodes.Ldstr || instruction.Operand is not string value)
            continue;
        if (value == "请输入您要存储的铜钱数量：" || value == "请输入您要取出的铜钱数量：")
            instruction.Operand = "请输入铜钱数量：";
    }

    var destroy = storeUI.Methods.Single(m => m.Name == "Destroy");
    var destroyIL = destroy.Body.GetILProcessor();
    var destroyStart = destroy.Body.Instructions[0];
    destroyIL.InsertBefore(destroyStart, Instruction.Create(OpCodes.Ldc_I4_0));
    destroyIL.InsertBefore(destroyStart, Instruction.Create(OpCodes.Stsfld, mode));
    destroyIL.InsertBefore(destroyStart, Instruction.Create(OpCodes.Ldc_I4_0));
    destroyIL.InsertBefore(destroyStart, Instruction.Create(OpCodes.Stsfld,
        storeUI.Fields.Single(f => f.Name == "CodexTradeConfigured")));
    PatchTwoSidedTradeStore(module, storeUI, mode);
}

static void BuildTradeUIConfigurator(ModuleDefinition module, TypeDefinition storeUI, FieldDefinition mode,
    MethodDefinition closeTrade, MethodDefinition configure)
{
    configure.Body.Instructions.Clear();
    configure.Body.Variables.Clear();
    configure.Body.ExceptionHandlers.Clear();
    configure.Body.InitLocals = true;

    var fui = FindType(module, "ET.FUI_StoreUI");
    var uiField = storeUI.Fields.Single(f => f.Name == "ui");
    uiField.Attributes = (uiField.Attributes & ~FieldAttributes.FieldAccessMask) | FieldAttributes.Public;
    var configured = storeUI.Fields.SingleOrDefault(field => field.Name == "CodexTradeConfigured")
        ?? new FieldDefinition("CodexTradeConfigured", FieldAttributes.Public | FieldAttributes.Static,
            module.TypeSystem.Boolean);
    if (configured.DeclaringType == null)
        storeUI.Fields.Add(configured);
    configure.Attributes = (configure.Attributes & ~MethodAttributes.MemberAccessMask) | MethodAttributes.Public;
    var getOnClick = FindMethodReference(module, "FairyGUI.GObject", "get_onClick");
    var add = FindMethodReference(module, "FairyGUI.EventListener", "Add");
    var set = FindMethodReference(module, "FairyGUI.EventListener", "Set");
    var callbackCtor = module.GetMemberReferences().OfType<MethodReference>().First(m =>
        m.Name == ".ctor" && m.DeclaringType.FullName == "FairyGUI.EventCallback0");
    var setTitle = FindMethodReference(module, "FairyGUI.GButton", "set_title");
    var setLabelTitle = FindMethodReference(module, "FairyGUI.GLabel", "set_title");
    var setVisible = FindMethodReference(module, "FairyGUI.GObject", "set_visible");
    var setTouchable = FindMethodReference(module, "FairyGUI.GObject", "set_touchable");
    var setXY = module.GetMemberReferences().OfType<MethodReference>().First(method =>
        method.DeclaringType.FullName == "FairyGUI.GObject" && method.Name == "SetXY" && method.Parameters.Count == 2);
    var setSize = module.GetMemberReferences().OfType<MethodReference>().First(method =>
        method.DeclaringType.FullName == "FairyGUI.GObject" && method.Name == "SetSize" && method.Parameters.Count == 2);
    var gObject = module.GetTypeReferences().First(type => type.FullName == "FairyGUI.GObject");
    var getX = new MethodReference("get_x", module.TypeSystem.Single, gObject) { HasThis = true };
    var setX = new MethodReference("set_x", module.TypeSystem.Void, gObject) { HasThis = true };
    setX.Parameters.Add(new ParameterDefinition(module.TypeSystem.Single));
    var currency = EnsureTradeCurrencyControls(module, storeUI);
    var done = Instruction.Create(OpCodes.Ret);
    var il = configure.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldsfld, mode));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldsfld, configured));
    il.Append(Instruction.Create(OpCodes.Brtrue, done));

    var frameField = fui.Fields.Single(f => f.Name == "m_frame");
    var frame = frameField.FieldType.Resolve();
    EmitLoadUIField(il, uiField, frameField);
    il.Append(Instruction.Create(OpCodes.Ldfld, frame.Fields.Single(f => f.Name == "self")));
    il.Append(Instruction.Create(OpCodes.Ldstr, "交易"));
    il.Append(Instruction.Create(OpCodes.Callvirt, setLabelTitle));

    foreach (var (fieldName, title) in new[]
    {
        ("m_btnLast", "我的报价"), ("m_btnNext", "对方报价"),
        ("m_btnSaveCoin", "加金币"), ("m_btnGetCoin", "减金币"),
        ("m_btnSort", "锁定交易"), ("m_btnExtand", "取消交易"),
    })
    {
        var field = fui.Fields.Single(f => f.Name == fieldName);
        EmitLoadUIField(il, uiField, field);
        il.Append(Instruction.Create(OpCodes.Ldfld, field.FieldType.Resolve().Fields.Single(f => f.Name == "self")));
        il.Append(Instruction.Create(OpCodes.Ldstr, title));
        il.Append(Instruction.Create(OpCodes.Callvirt, setTitle));
    }

    foreach (var (fieldName, x, y, width, height, touchable) in new[]
    {
        ("m_btnLast", 73f, 45f, 260f, 30f, false),
        ("m_btnNext", 357f, 45f, 260f, 30f, false),
        ("m_btnSaveCoin", 70f, 466f, 86f, 30f, true),
        ("m_btnGetCoin", 360f, 466f, 86f, 30f, true),
        ("m_btnSort", 180f, 503f, 155f, 34f, true),
        ("m_btnExtand", 355f, 503f, 155f, 34f, true),
    })
    {
        var field = fui.Fields.Single(f => f.Name == fieldName);
        EmitLoadUIField(il, uiField, field);
        il.Append(Instruction.Create(OpCodes.Ldfld, field.FieldType.Resolve().Fields.Single(f => f.Name == "self")));
        il.Append(Instruction.Create(OpCodes.Ldc_R4, x));
        il.Append(Instruction.Create(OpCodes.Ldc_R4, y));
        il.Append(Instruction.Create(OpCodes.Callvirt, setXY));
        EmitLoadUIField(il, uiField, field);
        il.Append(Instruction.Create(OpCodes.Ldfld, field.FieldType.Resolve().Fields.Single(f => f.Name == "self")));
        il.Append(Instruction.Create(OpCodes.Ldc_R4, width));
        il.Append(Instruction.Create(OpCodes.Ldc_R4, height));
        il.Append(Instruction.Create(OpCodes.Callvirt, setSize));
        if (!touchable)
        {
            EmitLoadUIField(il, uiField, field);
            il.Append(Instruction.Create(OpCodes.Ldfld, field.FieldType.Resolve().Fields.Single(f => f.Name == "self")));
            il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
            il.Append(Instruction.Create(OpCodes.Callvirt, setTouchable));
        }
    }

    var page = fui.Fields.Single(f => f.Name == "m_txtPage");
    EmitLoadUIField(il, uiField, page);
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 75f));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 435f));
    il.Append(Instruction.Create(OpCodes.Callvirt, setXY));
    EmitLoadUIField(il, uiField, page);
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 550f));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 26f));
    il.Append(Instruction.Create(OpCodes.Callvirt, setSize));

    foreach (var fieldName in new[] { "m_txtGold", "m_txtSliver", "m_txtCoin" })
    {
        EmitLoadUIField(il, uiField, fui.Fields.Single(f => f.Name == fieldName));
        il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
        il.Append(Instruction.Create(OpCodes.Callvirt, setVisible));
    }
    var getChild = module.GetMemberReferences().OfType<MethodReference>().First(method =>
        method.DeclaringType.FullName == "FairyGUI.GComponent" && method.Name == "GetChild");
    foreach (var childName in new[] { "n5", "n6", "n7", "n20" })
    {
        EmitLoadUIField(il, uiField, fui.Fields.Single(f => f.Name == "self"));
        il.Append(Instruction.Create(OpCodes.Ldstr, childName));
        il.Append(Instruction.Create(OpCodes.Callvirt, getChild));
        il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
        il.Append(Instruction.Create(OpCodes.Callvirt, setVisible));
    }

    var searchInput = fui.Fields.Single(f => f.Name == "m_iptSearch");
    EmitLoadUIField(il, uiField, searchInput);
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, setVisible));
    var searchButton = fui.Fields.Single(f => f.Name == "m_btnSearch");
    EmitLoadUIField(il, uiField, searchButton);
    il.Append(Instruction.Create(OpCodes.Ldfld, searchButton.FieldType.Resolve().Fields.Single(f => f.Name == "self")));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, setVisible));

    BindTradeButton(il, uiField, fui.Fields.Single(f => f.Name == "m_btnSaveCoin"),
        currency.addGold, getOnClick, callbackCtor, set);
    BindTradeButton(il, uiField, fui.Fields.Single(f => f.Name == "m_btnGetCoin"),
        currency.removeGold, getOnClick, callbackCtor, set);

    var template = fui.Fields.Single(f => f.Name == "m_btnSaveCoin");
    foreach (var (field, title, x, callback) in new[]
    {
        (currency.addSilverButton, "加银币", 162f, currency.addSilver),
        (currency.addCopperButton, "加铜币", 254f, currency.addCopper),
        (currency.removeSilverButton, "减银币", 452f, currency.removeSilver),
        (currency.removeCopperButton, "减铜币", 544f, currency.removeCopper),
    })
    {
        il.Append(Instruction.Create(OpCodes.Ldarg_0));
        // Keep the StoreUI receiver for stfld after the instance factory call.
        il.Append(Instruction.Create(OpCodes.Dup));
        EmitLoadUIField(il, uiField, template);
        il.Append(Instruction.Create(OpCodes.Ldfld, template.FieldType.Resolve().Fields.Single(f => f.Name == "self")));
        il.Append(Instruction.Create(OpCodes.Ldstr, title));
        il.Append(Instruction.Create(OpCodes.Ldc_R4, x));
        il.Append(Instruction.Create(OpCodes.Ldc_R4, 466f));
        il.Append(Instruction.Create(OpCodes.Ldarg_0));
        il.Append(Instruction.Create(OpCodes.Ldftn, callback));
        il.Append(Instruction.Create(OpCodes.Newobj, callbackCtor));
        il.Append(Instruction.Create(OpCodes.Call, currency.createButton));
        il.Append(Instruction.Create(OpCodes.Stfld, field));
    }

    EmitTradeDivider(module, storeUI, fui, uiField, configure, il);

    var self = fui.Fields.Single(f => f.Name == "self");
    EmitLoadUIField(il, uiField, self);
    EmitLoadUIField(il, uiField, self);
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getX)));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 360f));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setX)));

    var closeButton = frame.Fields.Single(f => f.Name == "m_closeButton");
    EmitLoadUIField(il, uiField, frameField);
    il.Append(Instruction.Create(OpCodes.Ldfld, closeButton));
    il.Append(Instruction.Create(OpCodes.Ldfld, closeButton.FieldType.Resolve().Fields.Single(f => f.Name == "self")));
    il.Append(Instruction.Create(OpCodes.Callvirt, getOnClick));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldftn, closeTrade));
    il.Append(Instruction.Create(OpCodes.Newobj, callbackCtor));
    il.Append(Instruction.Create(OpCodes.Callvirt, add));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Stsfld, configured));
    il.Append(done);
}

static void BindTradeButton(ILProcessor il, FieldDefinition uiField, FieldDefinition button,
    MethodDefinition callback, MethodReference getOnClick, MethodReference callbackCtor, MethodReference set)
{
    EmitLoadUIField(il, uiField, button);
    il.Append(Instruction.Create(OpCodes.Ldfld, button.FieldType.Resolve().Fields.Single(field => field.Name == "self")));
    il.Append(Instruction.Create(OpCodes.Callvirt, getOnClick));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldftn, callback));
    il.Append(Instruction.Create(OpCodes.Newobj, callbackCtor));
    il.Append(Instruction.Create(OpCodes.Callvirt, set));
}

static (MethodDefinition addGold, MethodDefinition addSilver, MethodDefinition addCopper,
    MethodDefinition removeGold, MethodDefinition removeSilver, MethodDefinition removeCopper,
    MethodDefinition createButton, FieldDefinition addSilverButton, FieldDefinition addCopperButton,
    FieldDefinition removeSilverButton, FieldDefinition removeCopperButton)
    EnsureTradeCurrencyControls(ModuleDefinition module, TypeDefinition storeUI)
{
    var addPopup = storeUI.Methods.Single(method => method.Name == "<AwakeAsync>b__7_4");
    var removePopup = storeUI.Methods.Single(method => method.Name == "<AwakeAsync>b__7_5");
    var sendAdd = storeUI.Methods.Single(method => method.Name == "<AwakeAsync>g__SendPutProto|7_13");
    var sendRemove = storeUI.Methods.Single(method => method.Name == "<AwakeAsync>g__SendPutProto|7_15");
    var amountType = module.ImportReference(sendAdd.Parameters.Single().ParameterType);
    if (sendRemove.Parameters.Single().ParameterType.FullName != amountType.FullName)
        throw new InvalidOperationException("store coin add/remove amount types differ");

    var factor = storeUI.Fields.SingleOrDefault(field => field.Name == "CodexTradeCoinFactor")
        ?? new FieldDefinition("CodexTradeCoinFactor", FieldAttributes.Private | FieldAttributes.Static, amountType);
    if (factor.DeclaringType == null)
        storeUI.Fields.Add(factor);
    var prompt = storeUI.Fields.SingleOrDefault(field => field.Name == "CodexTradeCoinPrompt")
        ?? new FieldDefinition("CodexTradeCoinPrompt", FieldAttributes.Private | FieldAttributes.Static, module.TypeSystem.String);
    if (prompt.DeclaringType == null)
        storeUI.Fields.Add(prompt);

    PatchCoinPopupPrompt(addPopup, prompt);
    PatchCoinPopupPrompt(removePopup, prompt);
    PatchCoinAmountMultiplier(sendAdd, factor);
    PatchCoinAmountMultiplier(sendRemove, factor);

    MethodDefinition EnsureAction(string name, MethodDefinition popup, long denomination, string message)
    {
        var existing = storeUI.Methods.SingleOrDefault(method => method.Name == name);
        if (existing != null)
            return existing;
        var method = new MethodDefinition(name,
            MethodAttributes.Private | MethodAttributes.HideBySig, module.TypeSystem.Void);
        storeUI.Methods.Add(method);
        var il = method.Body.GetILProcessor();
        il.Append(amountType.MetadataType == MetadataType.Int64
            ? Instruction.Create(OpCodes.Ldc_I8, denomination)
            : Instruction.Create(OpCodes.Ldc_I4, checked((int)denomination)));
        il.Append(Instruction.Create(OpCodes.Stsfld, factor));
        il.Append(Instruction.Create(OpCodes.Ldstr, message));
        il.Append(Instruction.Create(OpCodes.Stsfld, prompt));
        il.Append(Instruction.Create(OpCodes.Ldarg_0));
        il.Append(Instruction.Create(OpCodes.Call, popup));
        il.Append(Instruction.Create(OpCodes.Ret));
        return method;
    }

    var addGold = EnsureAction("Codex_AddGold", addPopup, 10000, "请输入要添加的金币数量（1金=10000铜）：");
    var addSilver = EnsureAction("Codex_AddSilver", addPopup, 100, "请输入要添加的银币数量（1银=100铜）：");
    var addCopper = EnsureAction("Codex_AddCopper", addPopup, 1, "请输入要添加的铜币数量：");
    var removeGold = EnsureAction("Codex_RemoveGold", removePopup, 10000, "请输入要撤回的金币数量（1金=10000铜）：");
    var removeSilver = EnsureAction("Codex_RemoveSilver", removePopup, 100, "请输入要撤回的银币数量（1银=100铜）：");
    var removeCopper = EnsureAction("Codex_RemoveCopper", removePopup, 1, "请输入要撤回的铜币数量：");

    var awakeState = storeUI.NestedTypes.Single(type => type.Name == "<AwakeAsync>d__7");
    var awakeMove = awakeState.Methods.Single(method => method.Name == "MoveNext");
    foreach (var instruction in awakeMove.Body.Instructions)
    {
        if (instruction.Operand is not MethodReference reference)
            continue;
        if (reference.FullName == addPopup.FullName)
            instruction.Operand = addGold;
        else if (reference.FullName == removePopup.FullName)
            instruction.Operand = removeGold;
    }

    var gButton = module.GetTypeReferences().First(type => type.FullName == "FairyGUI.GButton");
    FieldDefinition EnsureButton(string name)
    {
        var field = storeUI.Fields.SingleOrDefault(value => value.Name == name)
            ?? new FieldDefinition(name, FieldAttributes.Private, module.ImportReference(gButton));
        if (field.DeclaringType == null)
            storeUI.Fields.Add(field);
        return field;
    }
    var createButton = storeUI.Methods.SingleOrDefault(method => method.Name == "Codex_CreateTradeCoinButton")
        ?? BuildTradeCoinButtonFactory(module, storeUI, gButton);
    return (addGold, addSilver, addCopper, removeGold, removeSilver, removeCopper, createButton,
        EnsureButton("CodexAddSilverButton"), EnsureButton("CodexAddCopperButton"),
        EnsureButton("CodexRemoveSilverButton"), EnsureButton("CodexRemoveCopperButton"));
}

static void PatchCoinPopupPrompt(MethodDefinition popup, FieldDefinition prompt)
{
    if (popup.Body.Instructions.Any(value => value.OpCode == OpCodes.Ldsfld
        && value.Operand is FieldReference field && field.Name == prompt.Name))
        return;
    var instruction = popup.Body.Instructions.First(value => value.OpCode == OpCodes.Ldstr
        && value.Operand is string text && text.Contains("数量"));
    instruction.OpCode = OpCodes.Ldsfld;
    instruction.Operand = prompt;
}

static void PatchCoinAmountMultiplier(MethodDefinition method, FieldDefinition factor)
{
    var load = method.Body.Instructions.First(instruction => instruction.OpCode == OpCodes.Ldarg_1
        && new[] { instruction.Next, instruction.Next?.Next, instruction.Next?.Next?.Next }
            .Any(next => next?.OpCode == OpCodes.Stfld
                && next.Operand is FieldReference field && field.Name == "count"));
    if (load.Next?.OpCode == OpCodes.Ldsfld
        && load.Next.Operand is FieldReference existing && existing.Name == factor.Name)
        return;
    var il = method.Body.GetILProcessor();
    il.InsertAfter(load, Instruction.Create(OpCodes.Ldsfld, factor));
    il.InsertAfter(load.Next!, Instruction.Create(OpCodes.Mul));
}

static MethodDefinition BuildTradeCoinButtonFactory(ModuleDefinition module, TypeDefinition storeUI, TypeReference gButton)
{
    var method = new MethodDefinition("Codex_CreateTradeCoinButton",
        MethodAttributes.Private | MethodAttributes.HideBySig, module.ImportReference(gButton));
    method.Parameters.Add(new ParameterDefinition("template", ParameterAttributes.None, module.ImportReference(gButton)));
    method.Parameters.Add(new ParameterDefinition("title", ParameterAttributes.None, module.TypeSystem.String));
    method.Parameters.Add(new ParameterDefinition("x", ParameterAttributes.None, module.TypeSystem.Single));
    method.Parameters.Add(new ParameterDefinition("y", ParameterAttributes.None, module.TypeSystem.Single));
    var callbackType = module.GetTypeReferences().First(type => type.FullName == "FairyGUI.EventCallback0");
    method.Parameters.Add(new ParameterDefinition("callback", ParameterAttributes.None, module.ImportReference(callbackType)));
    method.Body.InitLocals = true;
    method.Body.Variables.Add(new VariableDefinition(module.ImportReference(gButton)));
    storeUI.Methods.Add(method);

    var gObject = module.GetTypeReferences().First(type => type.FullName == "FairyGUI.GObject");
    var uiPackage = module.GetTypeReferences().First(type => type.FullName == "FairyGUI.UIPackage");
    var getResource = new MethodReference("get_resourceURL", module.TypeSystem.String, module.ImportReference(gObject)) { HasThis = true };
    var create = new MethodReference("CreateObjectFromURL", module.ImportReference(gObject), module.ImportReference(uiPackage)) { HasThis = false };
    create.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    var asButton = new MethodReference("get_asButton", module.ImportReference(gButton), module.ImportReference(gObject)) { HasThis = true };
    var setTitle = FindMethodReference(module, "FairyGUI.GButton", "set_title");
    var setXY = module.GetMemberReferences().OfType<MethodReference>().First(value =>
        value.DeclaringType.FullName == "FairyGUI.GObject" && value.Name == "SetXY" && value.Parameters.Count == 2);
    var setSize = module.GetMemberReferences().OfType<MethodReference>().First(value =>
        value.DeclaringType.FullName == "FairyGUI.GObject" && value.Name == "SetSize" && value.Parameters.Count == 2);
    var getOnClick = FindMethodReference(module, "FairyGUI.GObject", "get_onClick");
    var set = FindMethodReference(module, "FairyGUI.EventListener", "Set");
    var fui = FindType(module, "ET.FUI_StoreUI");
    var fuiSelf = fui.Fields.Single(field => field.Name == "self");
    var ui = storeUI.Fields.Single(field => field.Name == "ui");
    var addChild = module.GetMemberReferences().OfType<MethodReference>().First(value =>
        value.DeclaringType.FullName == "FairyGUI.GComponent" && value.Name == "AddChild");
    var il = method.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Callvirt, getResource));
    il.Append(Instruction.Create(OpCodes.Call, create));
    il.Append(Instruction.Create(OpCodes.Callvirt, asButton));
    il.Append(Instruction.Create(OpCodes.Stloc_0));
    il.Append(Instruction.Create(OpCodes.Ldloc_0));
    il.Append(Instruction.Create(OpCodes.Ldarg_2));
    il.Append(Instruction.Create(OpCodes.Callvirt, setTitle));
    il.Append(Instruction.Create(OpCodes.Ldloc_0));
    il.Append(Instruction.Create(OpCodes.Ldarg_3));
    il.Append(Instruction.Create(OpCodes.Ldarg_S, method.Parameters[3]));
    il.Append(Instruction.Create(OpCodes.Callvirt, setXY));
    il.Append(Instruction.Create(OpCodes.Ldloc_0));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 86f));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 30f));
    il.Append(Instruction.Create(OpCodes.Callvirt, setSize));
    il.Append(Instruction.Create(OpCodes.Ldloc_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, getOnClick));
    il.Append(Instruction.Create(OpCodes.Ldarg_S, method.Parameters[4]));
    il.Append(Instruction.Create(OpCodes.Callvirt, set));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, ui));
    il.Append(Instruction.Create(OpCodes.Ldfld, fuiSelf));
    il.Append(Instruction.Create(OpCodes.Ldloc_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, addChild));
    il.Append(Instruction.Create(OpCodes.Pop));
    il.Append(Instruction.Create(OpCodes.Ldloc_0));
    il.Append(Instruction.Create(OpCodes.Ret));
    return method;
}

static void EmitTradeDivider(ModuleDefinition module, TypeDefinition storeUI, TypeDefinition fui,
    FieldDefinition uiField, MethodDefinition configure, ILProcessor il)
{
    var graphType = module.GetTypeReferences().First(type => type.FullName == "FairyGUI.GGraph");
    var graphField = storeUI.Fields.SingleOrDefault(field => field.Name == "CodexTradeDivider")
        ?? new FieldDefinition("CodexTradeDivider", FieldAttributes.Private, module.ImportReference(graphType));
    if (graphField.DeclaringType == null)
        storeUI.Fields.Add(graphField);
    var colorType = module.GetTypeReferences().First(type => type.FullName == "UnityEngine.Color");
    var lineColor = new VariableDefinition(module.ImportReference(colorType));
    var fillColor = new VariableDefinition(module.ImportReference(colorType));
    configure.Body.Variables.Add(lineColor);
    configure.Body.Variables.Add(fillColor);
    var colorCtor = module.ImportReference(colorType.Resolve().Methods.First(method =>
        method.Name == ".ctor" && method.Parameters.Count == 4));
    var graphDefinition = graphType.Resolve();
    var graphCtor = module.ImportReference(graphDefinition.Methods.First(method =>
        method.Name == ".ctor" && method.Parameters.Count == 0));
    var drawRect = module.ImportReference(graphDefinition.Methods.First(method =>
        method.Name == "DrawRect" && method.Parameters.Count == 5));
    var setXY = module.GetMemberReferences().OfType<MethodReference>().First(method =>
        method.DeclaringType.FullName == "FairyGUI.GObject" && method.Name == "SetXY" && method.Parameters.Count == 2);
    var setTouchable = FindMethodReference(module, "FairyGUI.GObject", "set_touchable");
    var addChild = module.GetMemberReferences().OfType<MethodReference>().First(method =>
        method.DeclaringType.FullName == "FairyGUI.GComponent" && method.Name == "AddChild");
    var fuiSelf = fui.Fields.Single(field => field.Name == "self");

    EmitColor(il, lineColor, colorCtor, 0.84f, 0.68f, 0.25f, 0.95f);
    EmitColor(il, fillColor, colorCtor, 0.84f, 0.68f, 0.25f, 0.7f);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Newobj, graphCtor));
    il.Append(Instruction.Create(OpCodes.Stfld, graphField));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, graphField));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 5f));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 358f));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, lineColor));
    il.Append(Instruction.Create(OpCodes.Ldloc, fillColor));
    il.Append(Instruction.Create(OpCodes.Callvirt, drawRect));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, graphField));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 342f));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, 78f));
    il.Append(Instruction.Create(OpCodes.Callvirt, setXY));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, graphField));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, setTouchable));
    EmitLoadUIField(il, uiField, fuiSelf);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, graphField));
    il.Append(Instruction.Create(OpCodes.Callvirt, addChild));
    il.Append(Instruction.Create(OpCodes.Pop));
}

static void EmitColor(ILProcessor il, VariableDefinition target, MethodReference constructor,
    float red, float green, float blue, float alpha)
{
    il.Append(Instruction.Create(OpCodes.Ldloca, target));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, red));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, green));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, blue));
    il.Append(Instruction.Create(OpCodes.Ldc_R4, alpha));
    il.Append(Instruction.Create(OpCodes.Call, constructor));
}

static void PatchTwoSidedTradeStore(ModuleDefinition module, TypeDefinition storeUI, FieldDefinition mode)
{
    // SendPutOutProto is also the normal warehouse take-out entry point.  The
    // old trade guard injected a second return path into this async wrapper;
    // Mono rejects that method body before the warehouse request is sent.
    NormalizeStoreTakeoutMethod(storeUI);
    NormalizeTradeLabels(storeUI);
    PatchTradeTooltipCallback(storeUI, mode);
    var configure = storeUI.Methods.Single(method => method.Name == "Codex_ConfigureTradeUI");
    var closeTrade = storeUI.Methods.Single(method => method.Name == "Codex_CloseTrade");
    BuildTradeUIConfigurator(module, storeUI, mode, closeTrade, configure);
    PatchTradeOpenEvent(module, mode, configure);
    foreach (var fieldName in new[] { "CodexNextRefresh", "CodexRefreshPending" })
    {
        var existing = storeUI.Fields.SingleOrDefault(field => field.Name == fieldName);
        if (existing != null)
            existing.Attributes = (existing.Attributes & ~FieldAttributes.FieldAccessMask) | FieldAttributes.Public;
    }
    var oldUpdateSystem = module.Types.SingleOrDefault(type => type.FullName == "ET.StoreUITradeUpdateSystem");
    if (oldUpdateSystem != null)
        module.Types.Remove(oldUpdateSystem);
    if (storeUI.Methods.Any(method => method.Name == "Codex_EndRemoteTrade"))
    {
        var existingNextRefresh = storeUI.Fields.Single(field => field.Name == "CodexNextRefresh");
        var existingRefreshPending = storeUI.Fields.Single(field => field.Name == "CodexRefreshPending");
        ResetTradeConfiguration(storeUI,
            storeUI.Fields.Single(field => field.Name == "CodexTradeConfigured"));
        TradeRefreshPatch.BuildUpdateSystem(module, storeUI, mode, existingNextRefresh, existingRefreshPending);
        return;
    }
    if (storeUI.Methods.Any(method => method.Name == "Codex_ShowTradeSnapshot"))
    {
        var existingNextRefresh = storeUI.Fields.Single(field => field.Name == "CodexNextRefresh");
        var existingRefreshPending = storeUI.Fields.Single(field => field.Name == "CodexRefreshPending");
        PatchRemoteTradeEnd(module, storeUI, mode, existingRefreshPending);
        ResetTradeConfiguration(storeUI,
            storeUI.Fields.Single(field => field.Name == "CodexTradeConfigured"));
        TradeRefreshPatch.BuildUpdateSystem(module, storeUI, mode, existingNextRefresh, existingRefreshPending);
        return;
    }
    var nextRefresh = new FieldDefinition("CodexNextRefresh", FieldAttributes.Public, module.TypeSystem.Int64);
    var refreshPending = new FieldDefinition("CodexRefreshPending", FieldAttributes.Public, module.TypeSystem.Boolean);
    storeUI.Fields.Add(nextRefresh);
    storeUI.Fields.Add(refreshPending);

    var responseType = module.GetTypeReferences().First(type => type.FullName == "ET.M2C_GetStore");
    var getSelfCoin = NewGetter(module, responseType, "SelfCoin", module.TypeSystem.Int64);
    var getOtherCoin = NewGetter(module, responseType, "OtherCoin", module.TypeSystem.Int64);
    var getSelfLocked = NewGetter(module, responseType, "SelfLocked", module.TypeSystem.Boolean);
    var getOtherLocked = NewGetter(module, responseType, "OtherLocked", module.TypeSystem.Boolean);
    var getSelfConfirmed = NewGetter(module, responseType, "SelfConfirmed", module.TypeSystem.Boolean);
    var getOtherConfirmed = NewGetter(module, responseType, "OtherConfirmed", module.TypeSystem.Boolean);

    var formatCoin = BuildCoinFormatter(module, storeUI);
    var offerState = BuildOfferStateFormatter(module, storeUI);
    var showSnapshot = new MethodDefinition("Codex_ShowTradeSnapshot",
        MethodAttributes.Private | MethodAttributes.HideBySig, module.TypeSystem.Void);
    var showSlot = storeUI.Methods.Single(method => method.Name == "ShowSlot");
    foreach (var parameter in showSlot.Parameters)
        showSnapshot.Parameters.Add(new ParameterDefinition(parameter.Name, parameter.Attributes, module.ImportReference(parameter.ParameterType)));
    showSnapshot.Parameters.Add(new ParameterDefinition("response", ParameterAttributes.None, responseType));
    storeUI.Methods.Add(showSnapshot);
    BuildSnapshotRenderer(module, storeUI, mode, showSlot, showSnapshot, formatCoin, offerState,
        getSelfCoin, getOtherCoin, getSelfLocked, getOtherLocked, getSelfConfirmed, getOtherConfirmed);

    var getState = storeUI.NestedTypes.Single(type => type.Name == "<GetStoreSlot>d__9");
    var getMoveNext = getState.Methods.Single(method => method.Name == "MoveNext");
    var showCall = getMoveNext.Body.Instructions.Single(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method && method.Name == "ShowSlot");
    var responseLocal = getMoveNext.Body.Variables.First(variable => variable.VariableType.FullName == responseType.FullName);
    getMoveNext.Body.GetILProcessor().InsertBefore(showCall, Instruction.Create(OpCodes.Ldloc, responseLocal));
    showCall.Operand = showSnapshot;
    ClearRefreshPendingOnCompletion(getState, getMoveNext, storeUI, refreshPending);

    var awake = storeUI.Methods.Single(method => method.Name == "Awake");
    var awakeStart = awake.Body.Instructions[0];
    var awakeIL = awake.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldarg_0), Instruction.Create(OpCodes.Ldsfld, mode), Instruction.Create(OpCodes.Stfld, refreshPending),
        Instruction.Create(OpCodes.Ldarg_0), Instruction.Create(OpCodes.Ldc_I8, 0L), Instruction.Create(OpCodes.Stfld, nextRefresh),
        Instruction.Create(OpCodes.Ldarg_0), Instruction.Create(OpCodes.Ldc_I4_0), Instruction.Create(OpCodes.Stfld, storeUI.Fields.Single(field => field.Name == "currPage")),
        Instruction.Create(OpCodes.Ldarg_0), Instruction.Create(OpCodes.Ldc_I4_1), Instruction.Create(OpCodes.Stfld, storeUI.Fields.Single(field => field.Name == "totalPage")),
    })
        awakeIL.InsertBefore(awakeStart, instruction);
    TradeRefreshPatch.BuildUpdateSystem(module, storeUI, mode, nextRefresh, refreshPending);
    PatchRemoteTradeEnd(module, storeUI, mode, refreshPending);
    ResetTradeConfiguration(storeUI,
        storeUI.Fields.Single(field => field.Name == "CodexTradeConfigured"));
}

static void RepairStoreTakeout(string input, string output)
{
    var inputPath = Path.GetFullPath(input);
    var outputPath = Path.GetFullPath(output);
    var resolver = new DefaultAssemblyResolver();
    resolver.AddSearchDirectory(Path.GetDirectoryName(inputPath)!);
    using var module = ModuleDefinition.ReadModule(inputPath, new ReaderParameters
    {
        InMemory = true,
        ReadSymbols = false,
        AssemblyResolver = resolver,
    });

    var storeUI = FindType(module, "ET.StoreUI");
    var sendTake = storeUI.Methods.Single(method => method.Name == "SendPutOutProto");
    // A repaired DLL is a valid input for the standalone repair command.  It
    // is intentionally treated as a no-op so the installer can be rerun.
    RemoveStoreTakeoutGuard(sendTake);
    if (HasStoreTakeoutGuard(sendTake))
        throw new InvalidOperationException("SendPutOutProto trade guard remains after repair");

    Directory.CreateDirectory(Path.GetDirectoryName(outputPath)!);
    module.Write(outputPath, new WriterParameters { WriteSymbols = false });
    Console.WriteLine($"repaired {outputPath}");
}

static void RepairRequestWindow(string input, string output)
{
    var inputPath = Path.GetFullPath(input);
    var outputPath = Path.GetFullPath(output);
    var resolver = new DefaultAssemblyResolver();
    resolver.AddSearchDirectory(Path.GetDirectoryName(inputPath)!);
    using var module = ModuleDefinition.ReadModule(inputPath, new ReaderParameters
    {
        InMemory = true,
        ReadSymbols = false,
        AssemblyResolver = resolver,
    });
    PatchRequestWindowComponentReuse(module);
    Directory.CreateDirectory(Path.GetDirectoryName(outputPath)!);
    module.Write(outputPath, new WriterParameters { WriteSymbols = false });
    Console.WriteLine($"repaired {outputPath}");
}

static void NormalizeStoreTakeoutMethod(TypeDefinition storeUI)
{
    var sendTake = storeUI.Methods.Single(method => method.Name == "SendPutOutProto");
    RemoveStoreTakeoutGuard(sendTake);
    if (HasStoreTakeoutGuard(sendTake))
        throw new InvalidOperationException("SendPutOutProto trade guard was not removed");
}

static bool RemoveStoreTakeoutGuard(MethodDefinition method)
{
    var instructions = method.Body.Instructions;
    if (instructions.Count < 10)
        return false;

    var prefix = instructions.Take(9).ToArray();
    var originalStart = instructions[9];
    if (prefix[0].OpCode != OpCodes.Ldsfld ||
        prefix[0].Operand is not FieldReference mode || mode.Name != "CodexTradeMode" ||
        prefix[1].OpCode != OpCodes.Brfalse || !ReferenceEquals(prefix[1].Operand, originalStart) ||
        prefix[2].OpCode != OpCodes.Ldarg_1 ||
        !IsLdcI4(prefix[3], 10) || prefix[4].OpCode != OpCodes.Rem ||
        !IsLdcI4(prefix[5], 5) || prefix[6].OpCode != OpCodes.Blt ||
        !ReferenceEquals(prefix[6].Operand, originalStart) ||
        prefix[7].OpCode != OpCodes.Call ||
        prefix[7].Operand is not MethodReference completed ||
        completed.DeclaringType.FullName != "ET.ETTask" || completed.Name != "get_CompletedTask" ||
        prefix[8].OpCode != OpCodes.Ret)
        return false;

    var il = method.Body.GetILProcessor();
    foreach (var instruction in prefix)
        il.Remove(instruction);
    return true;
}

static bool HasStoreTakeoutGuard(MethodDefinition method)
{
    var instructions = method.Body.Instructions;
    return instructions.Count >= 9 && instructions[0].OpCode == OpCodes.Ldsfld &&
        instructions[0].Operand is FieldReference mode && mode.Name == "CodexTradeMode" &&
        instructions[1].OpCode == OpCodes.Brfalse;
}

static bool IsLdcI4(Instruction instruction, int value)
{
    if (instruction.OpCode == OpCodes.Ldc_I4_M1)
        return value == -1;
    if (instruction.OpCode == OpCodes.Ldc_I4_0)
        return value == 0;
    if (instruction.OpCode == OpCodes.Ldc_I4_1)
        return value == 1;
    if (instruction.OpCode == OpCodes.Ldc_I4_2)
        return value == 2;
    if (instruction.OpCode == OpCodes.Ldc_I4_3)
        return value == 3;
    if (instruction.OpCode == OpCodes.Ldc_I4_4)
        return value == 4;
    if (instruction.OpCode == OpCodes.Ldc_I4_5)
        return value == 5;
    if (instruction.OpCode == OpCodes.Ldc_I4_6)
        return value == 6;
    if (instruction.OpCode == OpCodes.Ldc_I4_7)
        return value == 7;
    if (instruction.OpCode == OpCodes.Ldc_I4_8)
        return value == 8;
    if (instruction.OpCode == OpCodes.Ldc_I4_S && instruction.Operand is sbyte shortValue)
        return shortValue == value;
    return instruction.OpCode == OpCodes.Ldc_I4 && instruction.Operand is int longValue && longValue == value;
}

static void PatchTradeOpenEvent(ModuleDefinition module, FieldDefinition mode,
    MethodDefinition configure)
{
    var openStore = FindType(module, "ET.OpenStoreEvent");
    var state = openStore.NestedTypes.Single(type => type.Name == "<Run>d__0");
    var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
    if (moveNext.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method
        && method.Name == configure.Name
        && method.DeclaringType.FullName == configure.DeclaringType.FullName))
        return;

    var getResult = moveNext.Body.Instructions.FirstOrDefault(instruction =>
        instruction.OpCode == OpCodes.Callvirt
        && instruction.Operand is MethodReference method
        && method.Name == "GetResult");
    if (getResult == null || getResult.Next?.OpCode != OpCodes.Pop)
        throw new InvalidOperationException("trade store open result was not structurally recognized");

    var popResult = getResult.Next;
    var doneConfigure = Instruction.Create(OpCodes.Nop);
    var il = moveNext.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldsfld, mode),
        Instruction.Create(OpCodes.Brfalse, popResult),
        Instruction.Create(OpCodes.Call, configure),
        Instruction.Create(OpCodes.Br, doneConfigure),
        doneConfigure,
    })
        il.InsertBefore(popResult, instruction);
}

static void ResetTradeConfiguration(TypeDefinition storeUI, FieldDefinition configured)
{
    foreach (var methodName in new[] { "Destroy", "Codex_CloseTrade", "Codex_CancelOrExpand" })
    {
        var method = storeUI.Methods.SingleOrDefault(value => value.Name == methodName);
        if (method == null || method.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Stsfld
            && instruction.Operand is FieldReference field && field.Name == configured.Name))
            continue;
        var il = method.Body.GetILProcessor();
        foreach (var ret in method.Body.Instructions.Where(instruction => instruction.OpCode == OpCodes.Ret).ToList())
        {
            il.InsertBefore(ret, Instruction.Create(OpCodes.Ldc_I4_0));
            il.InsertBefore(ret, Instruction.Create(OpCodes.Stsfld, configured));
        }
    }
}

static void NormalizeTradeLabels(TypeDefinition storeUI)
{
    foreach (var instruction in AllTypes(storeUI).SelectMany(type => type.Methods)
        .Where(method => method.HasBody).SelectMany(method => method.Body.Instructions))
    {
        if (instruction.OpCode != OpCodes.Ldstr || instruction.Operand is not string value)
            continue;
        if (value is "添加银币" or "添加铜币")
            instruction.Operand = "添加金额";
        else if (value is "撤回银币" or "撤回铜币")
            instruction.Operand = "撤回金额";
    }
}

static void PatchTradeTooltipCallback(TypeDefinition storeUI, FieldDefinition mode)
{
    var callback = storeUI.NestedTypes.SelectMany(AllTypes).SelectMany(type => type.Methods)
        .Single(method => method.Name == "<AwakeAsync>b__9" && method.HasBody &&
            method.Body.Instructions.Any(instruction => instruction.Operand is MethodReference reference &&
                reference.Name == "get_StoreItemDic"));
    var open = callback.Body.Instructions.Single(instruction => instruction.OpCode == OpCodes.Call &&
        instruction.Operand is MethodReference method && method.Name == "OpenUI" &&
        method.Parameters.Count == 4);
    var comparison = open.Previous?.Previous?.Previous
        ?? throw new InvalidOperationException("store tooltip comparison flag was not found");
    if (comparison.OpCode == OpCodes.Ldsfld)
        return;
    if (comparison.OpCode != OpCodes.Ldc_I4_1)
        throw new InvalidOperationException("store tooltip callback was not structurally recognized");
    comparison.OpCode = OpCodes.Ldsfld;
    comparison.Operand = mode;
    var il = callback.Body.GetILProcessor();
    il.InsertAfter(comparison, Instruction.Create(OpCodes.Ldc_I4_0));
    il.InsertAfter(comparison.Next!, Instruction.Create(OpCodes.Ceq));
}

static void PatchRemoteTradeEnd(ModuleDefinition module, TypeDefinition storeUI, FieldDefinition mode,
    FieldDefinition refreshPending)
{
    var end = new MethodDefinition("Codex_EndRemoteTrade",
        MethodAttributes.Private | MethodAttributes.HideBySig, module.TypeSystem.Void);
    storeUI.Methods.Add(end);
    var ui = storeUI.Fields.Single(field => field.Name == "ui");
    var getWindow = module.Types.SelectMany(AllTypes).SelectMany(type => type.Methods)
        .Where(method => method.HasBody).SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand).OfType<GenericInstanceMethod>()
        .First(method => method.Name == "GetComponent" && method.GenericArguments.Count == 1
            && method.GenericArguments[0].FullName == "ET.FUIWindowComponent");
    var windowField = module.Types.SelectMany(AllTypes).SelectMany(type => type.Methods)
        .Where(method => method.HasBody).SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand).OfType<FieldReference>()
        .First(field => field.Name == "Window");
    var il = end.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stsfld, mode));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stfld, refreshPending));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, ui));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getWindow)));
    il.Append(Instruction.Create(OpCodes.Ldfld, module.ImportReference(windowField)));
    il.Append(Instruction.Create(OpCodes.Callvirt, FindMethodReference(module, "FairyGUI.Window", "Hide")));
    il.Append(Instruction.Create(OpCodes.Ret));

    var state = storeUI.NestedTypes.Single(type => type.Name == "<GetStoreSlot>d__9");
    var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
    var getMessage = moveNext.Body.Instructions.First(instruction => instruction.OpCode == OpCodes.Callvirt
        && instruction.Operand is MethodReference method && method.Name == "get_Message");
    var normal = getMessage.Previous ?? throw new InvalidOperationException("trade message load was not found");
    var response = moveNext.Body.Variables.First(variable => variable.VariableType.FullName == "ET.M2C_GetStore");
    var owner = state.Fields.Single(field => field.Name == "<>4__this");
    var isNullOrEmpty = moveNext.Body.Instructions.First(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method && method.DeclaringType.FullName == "ET.StringUtil"
        && method.Name == "IsNullOrEmpty").Operand as MethodReference
        ?? throw new InvalidOperationException("StringUtil.IsNullOrEmpty was not found");
    var moveIL = moveNext.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldsfld, mode), Instruction.Create(OpCodes.Brfalse, normal),
        Instruction.Create(OpCodes.Ldloc, response), Instruction.Create(OpCodes.Callvirt, (MethodReference)getMessage.Operand),
        Instruction.Create(OpCodes.Call, isNullOrEmpty), Instruction.Create(OpCodes.Brtrue, normal),
        Instruction.Create(OpCodes.Ldarg_0), Instruction.Create(OpCodes.Ldfld, owner), Instruction.Create(OpCodes.Call, end),
    })
        moveIL.InsertBefore(normal, instruction);
}

static MethodDefinition BuildCoinFormatter(ModuleDefinition module, TypeDefinition storeUI)
{
    var method = new MethodDefinition("Codex_FormatTradeCoin",
        MethodAttributes.Private | MethodAttributes.Static | MethodAttributes.HideBySig, module.TypeSystem.String);
    method.Parameters.Add(new ParameterDefinition("coin", ParameterAttributes.None, module.TypeSystem.Int64));
    storeUI.Methods.Add(method);
    var format = module.GetMemberReferences().OfType<MethodReference>().First(reference => reference.DeclaringType.FullName == "System.String"
        && reference.Name == "Format" && reference.Parameters.Count == 4);
    var il = method.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldstr, "{0}金 {1}银 {2}铜"));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I8, 10000L));
    il.Append(Instruction.Create(OpCodes.Div));
    il.Append(Instruction.Create(OpCodes.Box, module.TypeSystem.Int64));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I8, 100L));
    il.Append(Instruction.Create(OpCodes.Div));
    il.Append(Instruction.Create(OpCodes.Ldc_I8, 100L));
    il.Append(Instruction.Create(OpCodes.Rem));
    il.Append(Instruction.Create(OpCodes.Box, module.TypeSystem.Int64));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I8, 100L));
    il.Append(Instruction.Create(OpCodes.Rem));
    il.Append(Instruction.Create(OpCodes.Box, module.TypeSystem.Int64));
    il.Append(Instruction.Create(OpCodes.Call, format));
    il.Append(Instruction.Create(OpCodes.Ret));
    return method;
}

static MethodDefinition BuildOfferStateFormatter(ModuleDefinition module, TypeDefinition storeUI)
{
    var method = new MethodDefinition("Codex_FormatTradeState",
        MethodAttributes.Private | MethodAttributes.Static | MethodAttributes.HideBySig, module.TypeSystem.String);
    method.Parameters.Add(new ParameterDefinition("locked", ParameterAttributes.None, module.TypeSystem.Boolean));
    method.Parameters.Add(new ParameterDefinition("confirmed", ParameterAttributes.None, module.TypeSystem.Boolean));
    method.Parameters.Add(new ParameterDefinition("remote", ParameterAttributes.None, module.TypeSystem.Boolean));
    storeUI.Methods.Add(method);
    var locked = Instruction.Create(OpCodes.Ldarg_0);
    var open = Instruction.Create(OpCodes.Ldarg_2);
    var il = method.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Brfalse, locked));
    il.Append(Instruction.Create(OpCodes.Ldarg_2));
    il.Append(Instruction.Create(OpCodes.Brfalse, Instruction.Create(OpCodes.Ldstr, "我方 已确认")));
    var selfConfirmed = il.Body.Instructions.Last();
    il.Append(Instruction.Create(OpCodes.Ldstr, "对方 已确认"));
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(selfConfirmed.Operand as Instruction ?? throw new InvalidOperationException());
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(locked);
    il.Append(Instruction.Create(OpCodes.Brfalse, open));
    il.Append(Instruction.Create(OpCodes.Ldarg_2));
    var selfLocked = Instruction.Create(OpCodes.Ldstr, "我方 已锁定");
    il.Append(Instruction.Create(OpCodes.Brfalse, selfLocked));
    il.Append(Instruction.Create(OpCodes.Ldstr, "对方 已锁定"));
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(selfLocked);
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(open);
    var selfOpen = Instruction.Create(OpCodes.Ldstr, "我方 可修改");
    il.Append(Instruction.Create(OpCodes.Brfalse, selfOpen));
    il.Append(Instruction.Create(OpCodes.Ldstr, "对方 报价中"));
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(selfOpen);
    il.Append(Instruction.Create(OpCodes.Ret));
    return method;
}

static void BuildSnapshotRenderer(ModuleDefinition module, TypeDefinition storeUI, FieldDefinition mode,
    MethodDefinition showSlot, MethodDefinition showSnapshot, MethodDefinition formatCoin, MethodDefinition offerState,
    MethodReference getSelfCoin, MethodReference getOtherCoin, MethodReference getSelfLocked,
    MethodReference getOtherLocked, MethodReference getSelfConfirmed, MethodReference getOtherConfirmed)
{
    var fui = FindType(module, "ET.FUI_StoreUI");
    var uiField = storeUI.Fields.Single(field => field.Name == "ui");
    var currPage = storeUI.Fields.Single(field => field.Name == "currPage");
    var totalPage = storeUI.Fields.Single(field => field.Name == "totalPage");
    var setText = FindMethodReference(module, "FairyGUI.GObject", "set_text");
    var setTitle = FindMethodReference(module, "FairyGUI.GButton", "set_title");
    var format = module.GetMemberReferences().OfType<MethodReference>().First(reference => reference.DeclaringType.FullName == "System.String"
        && reference.Name == "Format" && reference.Parameters.Count == 3);
    var done = Instruction.Create(OpCodes.Ret);
    var il = showSnapshot.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldarg_2));
    il.Append(Instruction.Create(OpCodes.Ldarg_3));
    il.Append(Instruction.Create(OpCodes.Call, showSlot));
    il.Append(Instruction.Create(OpCodes.Ldsfld, mode));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stfld, currPage));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Stfld, totalPage));

    EmitLoadUIField(il, uiField, fui.Fields.Single(field => field.Name == "m_txtPage"));
    il.Append(Instruction.Create(OpCodes.Ldstr, "我方 {0} | 对方 {1}"));
    il.Append(Instruction.Create(OpCodes.Ldarg_S, showSnapshot.Parameters[3]));
    il.Append(Instruction.Create(OpCodes.Callvirt, getSelfCoin));
    il.Append(Instruction.Create(OpCodes.Call, formatCoin));
    il.Append(Instruction.Create(OpCodes.Ldarg_S, showSnapshot.Parameters[3]));
    il.Append(Instruction.Create(OpCodes.Callvirt, getOtherCoin));
    il.Append(Instruction.Create(OpCodes.Call, formatCoin));
    il.Append(Instruction.Create(OpCodes.Call, format));
    il.Append(Instruction.Create(OpCodes.Callvirt, setText));

    EmitTradeStateButton(il, uiField, fui.Fields.Single(field => field.Name == "m_btnLast"),
        showSnapshot.Parameters[3], getSelfLocked, getSelfConfirmed, false, offerState, setTitle);
    EmitTradeStateButton(il, uiField, fui.Fields.Single(field => field.Name == "m_btnNext"),
        showSnapshot.Parameters[3], getOtherLocked, getOtherConfirmed, true, offerState, setTitle);
    il.Append(done);
}

static void EmitTradeStateButton(ILProcessor il, FieldDefinition uiField, FieldDefinition button,
    ParameterDefinition response, MethodReference getLocked, MethodReference getConfirmed, bool remote,
    MethodDefinition formatter, MethodReference setTitle)
{
    EmitLoadUIField(il, uiField, button);
    il.Append(Instruction.Create(OpCodes.Ldfld, button.FieldType.Resolve().Fields.Single(field => field.Name == "self")));
    il.Append(Instruction.Create(OpCodes.Ldarg_S, response));
    il.Append(Instruction.Create(OpCodes.Callvirt, getLocked));
    il.Append(Instruction.Create(OpCodes.Ldarg_S, response));
    il.Append(Instruction.Create(OpCodes.Callvirt, getConfirmed));
    il.Append(Instruction.Create(remote ? OpCodes.Ldc_I4_1 : OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Call, formatter));
    il.Append(Instruction.Create(OpCodes.Callvirt, setTitle));
}

static void ClearRefreshPendingOnCompletion(TypeDefinition state, MethodDefinition moveNext,
    TypeDefinition storeUI, FieldDefinition refreshPending)
{
    var owner = state.Fields.Single(field => field.Name == "<>4__this");
    var il = moveNext.Body.GetILProcessor();
    foreach (var call in moveNext.Body.Instructions.Where(instruction =>
        instruction.OpCode == OpCodes.Call && instruction.Operand is MethodReference method &&
        method.DeclaringType.FullName == "ET.ETAsyncTaskMethodBuilder" &&
        (method.Name == "SetResult" || method.Name == "SetException")).ToList())
    {
        il.InsertBefore(call, Instruction.Create(OpCodes.Ldarg_0));
        il.InsertBefore(call, Instruction.Create(OpCodes.Ldfld, owner));
        il.InsertBefore(call, Instruction.Create(OpCodes.Ldc_I4_0));
        il.InsertBefore(call, Instruction.Create(OpCodes.Stfld, refreshPending));
    }
}

static MethodReference NewGetter(ModuleDefinition module, TypeReference declaringType, string name, TypeReference returnType)
    => new($"get_{name}", module.ImportReference(returnType), module.ImportReference(declaringType)) { HasThis = true };

static void EmitLoadUIField(ILProcessor il, FieldDefinition ui, FieldDefinition field)
{
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, ui));
    il.Append(Instruction.Create(OpCodes.Ldfld, field));
}

static void PatchTradeRequestTitle(ModuleDefinition module)
{
    var requestUI = FindType(module, "ET.TeamRequestUI");
    var state = requestUI.NestedTypes.Single(t => t.Name == "<AwakeAsync>d__4");
    var moveNext = state.Methods.Single(m => m.Name == "MoveNext");
    if (moveNext.Body.Instructions.Any(i => i.OpCode == OpCodes.Ldstr && Equals(i.Operand, "\u4EA4\u6613\u7533\u8BF7"))) return;
    var setTitle = moveNext.Body.Instructions.First(i => i.OpCode == OpCodes.Callvirt && i.Operand is MethodReference m && m.Name == "set_text");
    var toString = moveNext.Body.Instructions.Take(moveNext.Body.Instructions.IndexOf(setTitle)).Last(i => i.OpCode == OpCodes.Callvirt && i.Operand is MethodReference m && m.Name == "ToString");
    var normal = moveNext.Body.Instructions.Take(moveNext.Body.Instructions.IndexOf(toString)).Last(i => i.OpCode == OpCodes.Ldloc_1);
    var info = requestUI.Fields.Single(f => f.Name == "info");
    var id = module.GetMemberReferences().OfType<FieldReference>().First(f => f.DeclaringType.FullName == "ET.TeamRequestInfo" && f.Name == "Id");
    var il = moveNext.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldloc_1), Instruction.Create(OpCodes.Ldflda, info),
        Instruction.Create(OpCodes.Ldfld, id), Instruction.Create(OpCodes.Ldc_I8, 0L),
        Instruction.Create(OpCodes.Bge, normal), Instruction.Create(OpCodes.Ldstr, "\u4EA4\u6613\u7533\u8BF7"),
        Instruction.Create(OpCodes.Br, setTitle),
    })
        il.InsertBefore(normal, instruction);
}

static MethodDefinition CloneMethod(ModuleDefinition module, MethodDefinition source, string name)
{
    var destination = new MethodDefinition(name, source.Attributes, module.ImportReference(source.ReturnType));
    foreach (var parameter in source.Parameters) destination.Parameters.Add(new ParameterDefinition(parameter.Name, parameter.Attributes, module.ImportReference(parameter.ParameterType)));
    destination.Body.InitLocals = source.Body.InitLocals;
    var variables = new Dictionary<VariableDefinition, VariableDefinition>();
    foreach (var variable in source.Body.Variables) { var copy = new VariableDefinition(module.ImportReference(variable.VariableType)); destination.Body.Variables.Add(copy); variables[variable] = copy; }
    var labels = new Dictionary<Instruction, Instruction>();
    foreach (var instruction in source.Body.Instructions) { var copy = CloneInstruction(module, instruction, variables); destination.Body.Instructions.Add(copy); labels[instruction] = copy; }
    foreach (var instruction in source.Body.Instructions) { var copy = labels[instruction]; if (instruction.Operand is Instruction target) copy.Operand = labels[target]; else if (instruction.Operand is Instruction[] targets) copy.Operand = targets.Select(target => labels[target]).ToArray(); }
    source.DeclaringType!.Methods.Add(destination);
    return destination;
}

static Instruction CloneInstruction(ModuleDefinition module, Instruction source, IReadOnlyDictionary<VariableDefinition, VariableDefinition> variables) => source.Operand switch
{
    null => Instruction.Create(source.OpCode), sbyte value => Instruction.Create(source.OpCode, value), byte value => Instruction.Create(source.OpCode, (sbyte)value), int value => Instruction.Create(source.OpCode, value), long value => Instruction.Create(source.OpCode, value), float value => Instruction.Create(source.OpCode, value), double value => Instruction.Create(source.OpCode, value), string value => Instruction.Create(source.OpCode, value), VariableDefinition variable => Instruction.Create(source.OpCode, variables[variable]), ParameterDefinition parameter => Instruction.Create(source.OpCode, parameter), MethodReference method => Instruction.Create(source.OpCode, module.ImportReference(method)), FieldReference field => Instruction.Create(source.OpCode, module.ImportReference(field)), TypeReference type => Instruction.Create(source.OpCode, module.ImportReference(type)), Instruction => Instruction.Create(source.OpCode, Instruction.Create(OpCodes.Nop)), Instruction[] => Instruction.Create(source.OpCode, Array.Empty<Instruction>()), _ => throw new NotSupportedException($"unsupported operand {source.Operand.GetType().FullName}"),
};

static MethodReference MakeGenericCall(ModuleDefinition module, GenericInstanceMethod template, TypeReference argument)
{
    var method = new GenericInstanceMethod(module.ImportReference(template.ElementMethod));
    method.GenericArguments.Add(module.ImportReference(argument));
    return module.ImportReference(method);
}

static CustomAttribute CloneAttribute(ModuleDefinition module, CustomAttribute source)
{
    var clone = new CustomAttribute(module.ImportReference(source.Constructor));
    foreach (var argument in source.ConstructorArguments)
        clone.ConstructorArguments.Add(new CustomAttributeArgument(module.ImportReference(argument.Type), argument.Value));
    foreach (var field in source.Fields)
        clone.Fields.Add(new CustomAttributeNamedArgument(field.Name,
            new CustomAttributeArgument(module.ImportReference(field.Argument.Type), field.Argument.Value)));
    foreach (var property in source.Properties)
        clone.Properties.Add(new CustomAttributeNamedArgument(property.Name,
            new CustomAttributeArgument(module.ImportReference(property.Argument.Type), property.Argument.Value)));
    return clone;
}

static MethodReference FindMethodReference(ModuleDefinition module, string typeName, string methodName)
    => module.GetMemberReferences().OfType<MethodReference>().First(method =>
        method.DeclaringType.FullName == typeName && method.Name == methodName);

static TypeDefinition FindType(ModuleDefinition module, string name) => module.Types.SelectMany(AllTypes).Single(t => t.FullName == name);
static IEnumerable<TypeDefinition> AllTypes(TypeDefinition type) { yield return type; foreach (var nested in type.NestedTypes.SelectMany(AllTypes)) yield return nested; }
