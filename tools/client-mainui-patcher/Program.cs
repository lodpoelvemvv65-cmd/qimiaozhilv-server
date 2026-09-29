using Mono.Cecil;
using Mono.Cecil.Cil;

var mode = "full";
if (args.Length == 3 && args[0] is "--disable-melting-refresh" or "--enable-melting-refresh")
{
    mode = args[0];
    args = args[1..];
}
else if (args.Length != 2)
{
    Console.Error.WriteLine("usage: ClientMainUIPatcher [--disable-melting-refresh|--enable-melting-refresh] <input-dll> <output-dll>");
    return 2;
}

var inputPath = Path.GetFullPath(args[0]);
var outputPath = Path.GetFullPath(args[1]);
var inputDirectory = Path.GetDirectoryName(inputPath)!;
var resolver = new GameAssemblyResolver(inputDirectory);
resolver.AddSearchDirectory(inputDirectory);
var managedDirectory = Path.GetFullPath(Path.Combine(inputDirectory, "..", "..", "..", "Managed"));
if (Directory.Exists(managedDirectory))
    resolver.AddSearchDirectory(managedDirectory);
using var module = ModuleDefinition.ReadModule(inputPath, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
    AssemblyResolver = resolver,
});

if (mode != "full")
{
    if (mode == "--disable-melting-refresh")
        RemoveMeltingGemRefresh(module);
    else
        PatchMeltingGemRefresh(module);

    Directory.CreateDirectory(Path.GetDirectoryName(outputPath)!);
    module.Write(outputPath, new WriterParameters { WriteSymbols = false });
    ValidateMeltingGemRefresh(outputPath, resolver, mode == "--enable-melting-refresh");
    Console.WriteLine($"{mode} {inputPath} -> {outputPath}");
    return 0;
}

var mainUI = FindType(module, "ET.MainUI");
var fuiMainUI = FindType(module, "ET.FUI_MainUI");
var eventCallback0 = FindTypeReference(module, "FairyGUI.EventCallback0");
var closure = mainUI.NestedTypes.Single(type => type.Name == "<>c");
var socialCallback = closure.Methods.Single(method => method.Name == "<AwakeAsync>b__3_14");
var rankingCallback = closure.Methods.Single(method => method.Name == "<AwakeAsync>b__3_4");
var refreshBuffCallback = mainUI.Methods.Single(method => method.Name == "<AwakeAsync>b__3_0");
var openCharacterCallback = mainUI.Methods.Single(method => method.Name == "<AwakeAsync>b__3_10");

var openTeam = mainUI.Methods.SingleOrDefault(method => method.Name == "Codex_OpenTeam");
if (openTeam == null)
{
    openTeam = new MethodDefinition(
        "Codex_OpenTeam",
        MethodAttributes.Private | MethodAttributes.HideBySig,
        module.TypeSystem.Void);
    mainUI.Methods.Add(openTeam);
    CloneBody(module, socialCallback, openTeam);
}

var openQuest = mainUI.Methods.SingleOrDefault(method => method.Name == "Codex_OpenQuest");
if (openQuest == null)
{
    openQuest = new MethodDefinition(
        "Codex_OpenQuest",
        MethodAttributes.Private | MethodAttributes.HideBySig,
        module.TypeSystem.Void);
    mainUI.Methods.Add(openQuest);
    CloneBody(module, rankingCallback, openQuest);
    ConvertRankingPublishToQuest(module, openQuest);
}

var stateMachine = mainUI.NestedTypes.Single(type => type.Name == "<AwakeAsync>d__3");
var moveNext = stateMachine.Methods.Single(method => method.Name == "MoveNext");
var instructions = moveNext.Body.Instructions;
var teamField = fuiMainUI.Fields.Single(field => field.Name == "m_btnTeam");
var littleGameField = fuiMainUI.Fields.Single(field => field.Name == "m_btnLittleGame");
var mineHeadField = fuiMainUI.Fields.Single(field => field.Name == "m_mineHeadInfo");
var headInfo = FindType(module, "ET.FUI_HeadInfoItemMain");
var headButtonField = headInfo.Fields.Single(field => field.Name == "m_btn");

var teamFieldInstruction = instructions.Single(instruction =>
    instruction.OpCode == OpCodes.Ldfld && IsField(instruction.Operand, teamField));
var teamFieldIndex = instructions.IndexOf(teamFieldInstruction);
var teamDelegate = instructions
    .Skip(teamFieldIndex + 1)
    .Take(8)
    .Single(instruction => instruction.OpCode == OpCodes.Ldftn);
teamDelegate.Operand = openTeam;

var littleGameBindingExists = instructions.Any(instruction =>
    instruction.OpCode == OpCodes.Ldfld && IsField(instruction.Operand, littleGameField));
if (!littleGameBindingExists)
{
    var bindingStart = instructions[teamFieldIndex - 1];
    if (bindingStart.OpCode != OpCodes.Ldsfld)
        throw new InvalidOperationException("unexpected team binding start");

    var getOnClick = instructions[teamFieldIndex + 1].Operand as MethodReference
        ?? throw new InvalidOperationException("team get_onClick reference not found");
    var delegateCtor = instructions[teamFieldIndex + 4].Operand as MethodReference
        ?? throw new InvalidOperationException("EventCallback0 constructor not found");
    var setListener = instructions[teamFieldIndex + 5].Operand as MethodReference
        ?? throw new InvalidOperationException("EventListener.Set reference not found");
    if (delegateCtor.DeclaringType.FullName != eventCallback0.FullName)
        throw new InvalidOperationException("unexpected team delegate type");

    var il = moveNext.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldsfld, (FieldReference)bindingStart.Operand),
        Instruction.Create(OpCodes.Ldfld, littleGameField),
        Instruction.Create(OpCodes.Callvirt, getOnClick),
        Instruction.Create(OpCodes.Ldloc_1),
        Instruction.Create(OpCodes.Ldftn, openQuest),
        Instruction.Create(OpCodes.Newobj, delegateCtor),
        Instruction.Create(OpCodes.Callvirt, setListener),
    })
    {
        il.InsertBefore(bindingStart, instruction);
    }
}

var headButtonInstructions = instructions.Where(instruction =>
    instruction.OpCode == OpCodes.Ldfld && IsField(instruction.Operand, headButtonField)).ToList();
var headRightButton = headButtonInstructions.Single(instruction =>
{
    var index = instructions.IndexOf(instruction);
    return instructions.Skip(index + 1).Take(2).Any(candidate =>
        candidate.Operand is MethodReference method && method.Name == "get_onRightClick");
});
var headRightIndex = instructions.IndexOf(headRightButton);
var headRightDelegate = instructions.Skip(headRightIndex + 1).Take(8)
    .Single(instruction => instruction.OpCode == OpCodes.Ldftn);
// Preserve the original client behavior: right-clicking the level/status area
// asks the server for active buff timers (20045). It refreshes the top-left
// status icons through M2C_BattleChangeState; it does not open StateBuffUI.
headRightDelegate.Operand = refreshBuffCallback;

var headClickBindingExists = headButtonInstructions.Any(instruction =>
{
    var index = instructions.IndexOf(instruction);
    var binding = instructions.Skip(index + 1).Take(8).ToList();
    return binding.Any(candidate =>
            candidate.Operand is MethodReference method && method.Name == "get_onClick")
        && binding.Any(candidate =>
            candidate.OpCode == OpCodes.Ldftn
            && candidate.Operand is MethodReference method
            && method.Name == openCharacterCallback.Name);
});
if (!headClickBindingExists)
{
    var bindingStart = instructions[headRightIndex - 2];
    if (bindingStart.OpCode != OpCodes.Ldsfld)
        throw new InvalidOperationException("unexpected head binding start");
    var getOnClick = FindMethodReference(module, "FairyGUI.GObject", "get_onClick");
    var delegateCtor = instructions[headRightIndex + 4].Operand as MethodReference
        ?? throw new InvalidOperationException("head EventCallback0 constructor not found");
    var setListener = instructions[headRightIndex + 5].Operand as MethodReference
        ?? throw new InvalidOperationException("head EventListener.Set reference not found");
    var il = moveNext.Body.GetILProcessor();
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldsfld, (FieldReference)bindingStart.Operand),
        Instruction.Create(OpCodes.Ldfld, mineHeadField),
        Instruction.Create(OpCodes.Ldfld, headButtonField),
        Instruction.Create(OpCodes.Callvirt, getOnClick),
        Instruction.Create(OpCodes.Ldloc_1),
        Instruction.Create(OpCodes.Ldftn, openCharacterCallback),
        Instruction.Create(OpCodes.Newobj, delegateCtor),
        Instruction.Create(OpCodes.Callvirt, setListener),
    })
    {
        il.InsertBefore(bindingStart, instruction);
    }
}

PatchBagDiscard(module, resolver);
PatchMeltingGemRefresh(module);
PatchProjectileTweenCall(module, resolver);

Directory.CreateDirectory(Path.GetDirectoryName(outputPath)!);
module.Write(outputPath, new WriterParameters { WriteSymbols = false });
ValidatePatchedModule(outputPath, resolver);
Console.WriteLine($"patched {inputPath} -> {outputPath}");
Console.WriteLine("m_btnRank       -> original RankingUI");
Console.WriteLine("m_btnLittleGame -> Quest1UI");
Console.WriteLine("m_btnTeam       -> original Friend/Social UI");
Console.WriteLine("level area left -> CharacterUI");
Console.WriteLine("level area right -> original C2M_GetBuffTime status refresh");
Console.WriteLine("bag drag outside -> C2M_DeleteItem + M2C_SendBag refresh");
Console.WriteLine("melting success -> clear consumed gem selection");
Console.WriteLine("projectile DOMove -> installed DOTween TweenerCore signature");
return 0;

static void PatchProjectileTweenCall(ModuleDefinition module, IAssemblyResolver resolver)
{
    var playEffect = FindType(module, "ET.PlayEffectEvent");
    var playBullet = playEffect.Methods.Single(method => method.Name == "PlayBulletEffect");
    var domoveCall = playBullet.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method
        && method.DeclaringType.FullName == "DG.Tweening.ShortcutExtensions"
        && method.Name == "DOMove");

    var dotweenReference = module.AssemblyReferences.Single(reference => reference.Name == "DOTween");
    var dotweenModule = resolver.Resolve(dotweenReference).MainModule;
    var shortcutExtensions = dotweenModule.GetType("DG.Tweening.ShortcutExtensions")
        ?? throw new InvalidOperationException("DG.Tweening.ShortcutExtensions not found");
    var installedDomove = shortcutExtensions.Methods.Single(method =>
        method.Name == "DOMove"
        && method.Parameters.Count == 4
        && method.Parameters[0].ParameterType.FullName == "UnityEngine.Transform"
        && method.Parameters[1].ParameterType.FullName == "UnityEngine.Vector3"
        && method.Parameters[2].ParameterType.FullName == "System.Single"
        && method.Parameters[3].ParameterType.FullName == "System.Boolean");
    if (!installedDomove.ReturnType.FullName.StartsWith("DG.Tweening.Core.TweenerCore`3", StringComparison.Ordinal))
        throw new InvalidOperationException($"unexpected installed DOMove return type {installedDomove.ReturnType.FullName}");

    domoveCall.Operand = module.ImportReference(installedDomove);
}

static void PatchBagDiscard(ModuleDefinition module, IAssemblyResolver resolver)
{
    var bagUI = FindType(module, "ET.BagUI");
    var eventContext = FindTypeReference(module, "FairyGUI.EventContext");
    var discard = bagUI.Methods.SingleOrDefault(method => method.Name == "Codex_DiscardItem");
    if (discard == null)
    {
        discard = new MethodDefinition(
            "Codex_DiscardItem",
            MethodAttributes.Private | MethodAttributes.HideBySig,
            module.TypeSystem.Void);
        discard.Parameters.Add(new ParameterDefinition("context", ParameterAttributes.None, eventContext));
        bagUI.Methods.Add(discard);
    }

    // Rebuild the helper on every run. Older patched clients listened on
    // GRoot.onDrop but did not distinguish a direct root drop from a bubbled
    // child drop. Consequently, dragging an item into MeltingUI could also
    // reach the root listener and discard the source equipment.
    discard.Body = new MethodBody(discard);
    BuildDiscardBody(module, resolver, bagUI, discard);

    var getRoot = FindMethodReference(module, "FairyGUI.GRoot", "get_inst");
    var getOnDrop = FindMethodReference(module, "FairyGUI.GComponent", "get_onDrop");
    var callbackCtor = module.GetMemberReferences().OfType<MethodReference>().Single(method =>
        method.Name == ".ctor" && method.DeclaringType.FullName == "FairyGUI.EventCallback1");
    var add = FindMethodReference(module, "FairyGUI.EventListener", "Add1");
    var thirdPartyReference = module.AssemblyReferences.Single(reference => reference.Name == "Unity.ThirdParty");
    var eventListener = resolver.Resolve(thirdPartyReference).MainModule.GetType("FairyGUI.EventListener")
        ?? throw new InvalidOperationException("FairyGUI.EventListener not found");
    var remove = module.ImportReference(eventListener.Methods.Single(method =>
        method.Name == "Remove1" && method.Parameters.Count == 1));

    var awake = bagUI.NestedTypes.Single(type => type.Name == "<AwakeAsync>d__14")
        .Methods.Single(method => method.Name == "MoveNext");
    if (!awake.Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Ldftn
            && instruction.Operand is MethodReference method
            && method.Name == discard.Name))
    {
        var refreshMoney = awake.Body.Instructions.Single(instruction =>
            instruction.OpCode == OpCodes.Call
            && instruction.Operand is MethodReference method
            && method.Name == "RefreshMoney"
            && method.DeclaringType.FullName == bagUI.FullName);
        var il = awake.Body.GetILProcessor();
        foreach (var instruction in new[]
        {
            Instruction.Create(OpCodes.Call, getRoot),
            Instruction.Create(OpCodes.Callvirt, getOnDrop),
            Instruction.Create(OpCodes.Ldloc_1),
            Instruction.Create(OpCodes.Ldftn, discard),
            Instruction.Create(OpCodes.Newobj, callbackCtor),
            Instruction.Create(OpCodes.Callvirt, add),
        })
        {
            il.InsertBefore(refreshMoney, instruction);
        }
    }

    var destroy = bagUI.Methods.Single(method => method.Name == "Destroy");
    if (!destroy.Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Ldftn
            && instruction.Operand is MethodReference method
            && method.Name == discard.Name))
    {
        var ret = destroy.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Ret);
        var il = destroy.Body.GetILProcessor();
        foreach (var instruction in new[]
        {
            Instruction.Create(OpCodes.Call, getRoot),
            Instruction.Create(OpCodes.Callvirt, getOnDrop),
            Instruction.Create(OpCodes.Ldarg_0),
            Instruction.Create(OpCodes.Ldftn, discard),
            Instruction.Create(OpCodes.Newobj, callbackCtor),
            Instruction.Create(OpCodes.Callvirt, remove),
        })
        {
            il.InsertBefore(ret, instruction);
        }
    }
}

static void BuildDiscardBody(
    ModuleDefinition module,
    IAssemblyResolver resolver,
    TypeDefinition bagUI,
    MethodDefinition discard)
{
    var hotfixReference = module.AssemblyReferences.Single(reference => reference.Name == "Unity.Hotfix");
    var hotfix = resolver.Resolve(hotfixReference).MainModule;
    var clientItemData = hotfix.GetType("ET.ClientItemData")
        ?? throw new InvalidOperationException("ET.ClientItemData not found");
    var deleteRequest = hotfix.GetType("ET.C2M_DeleteItem")
        ?? throw new InvalidOperationException("ET.C2M_DeleteItem not found");
    var deleteResponse = hotfix.GetType("ET.M2C_DeleteItem")
        ?? throw new InvalidOperationException("ET.M2C_DeleteItem not found");

    var uidragArgs = FindType(module, "ET.UIDragArgs");
    var eventData = FindFieldReference(module, "FairyGUI.EventContext", "data");
    var thirdPartyReference = module.AssemblyReferences.Single(reference => reference.Name == "Unity.ThirdParty");
    var eventContextDefinition = resolver.Resolve(thirdPartyReference).MainModule.GetType("FairyGUI.EventContext")
        ?? throw new InvalidOperationException("FairyGUI.EventContext not found");
    var rootDefinition = resolver.Resolve(thirdPartyReference).MainModule.GetType("FairyGUI.GRoot")
        ?? throw new InvalidOperationException("FairyGUI.GRoot not found");
    var getInitiator = module.ImportReference(eventContextDefinition.Methods.Single(method =>
        method.Name == "get_initiator" && !method.HasParameters));
    var getTouchTarget = module.ImportReference(rootDefinition.Methods.Single(method =>
        method.Name == "get_touchTarget" && !method.HasParameters));
    var getRoot = FindMethodReference(module, "FairyGUI.GRoot", "get_inst");
    var uiType = uidragArgs.Fields.Single(field => field.Name == "uiType");
    var release = uidragArgs.Methods.Single(method => method.Name == "Release");
    var dragIndex = bagUI.Fields.Single(field => field.Name == "_dragIndex");
    var zoneScene = bagUI.Fields.Single(field => field.Name == "zoneScene");
    var count = module.ImportReference(clientItemData.Fields.Single(field => field.Name == "Count"));

    var getItemComponent = FindMethodReference(module, "ET.ClientItemDataComponent", "get_Instance");
    var getItemDictionary = FindMethodReference(module, "ET.ClientItemDataComponent", "get_ItemDic");
    var tryGetItem = FindUsedMethod(discard.Module, "TryGetValueByKey1", "ET.ClientItemData");
    var getSession = FindUsedGenericMethod(discard.Module, "GetComponent", "ET.SessionComponent");
    var callTemplate = FindUsedGenericMethod(discard.Module, "Call", "ET.M2C_ChangeItemPos");
    var callDelete = new GenericInstanceMethod(module.ImportReference(callTemplate.ElementMethod));
    callDelete.GenericArguments.Add(module.ImportReference(deleteResponse));

    var requestCtor = module.ImportReference(deleteRequest.Methods.Single(method => method.IsConstructor && !method.HasParameters));
    var setIndex = module.ImportReference(deleteRequest.Methods.Single(method => method.Name == "set_Index"));
    var setCount = module.ImportReference(deleteRequest.Methods.Single(method => method.Name == "set_Count"));

    discard.Body.InitLocals = true;
    var dataLocal = new VariableDefinition(module.TypeSystem.Object);
    var argsLocal = new VariableDefinition(uidragArgs);
    var itemLocal = new VariableDefinition(module.ImportReference(clientItemData));
    discard.Body.Variables.Add(dataLocal);
    discard.Body.Variables.Add(argsLocal);
    discard.Body.Variables.Add(itemLocal);

    var il = discard.Body.GetILProcessor();
    var done = Instruction.Create(OpCodes.Ret);
    // Drop events bubble. Only a drop whose original target is GRoot means
    // "outside every UI". A child target (gem slot, store, shortcut slot,
    // etc.) is a real operation and must never be interpreted as discard.
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Callvirt, getInitiator));
    il.Append(Instruction.Create(OpCodes.Call, getRoot));
    il.Append(Instruction.Create(OpCodes.Bne_Un, done));
    // DragDropManager climbs from the object under the mouse until it finds
    // an onDrop listener. Since this helper is installed on GRoot, a missed
    // gem/equipment target could otherwise climb all the way to GRoot and be
    // deleted. Require the actual object under the pointer to be GRoot too;
    // panels and controls are never discard zones.
    il.Append(Instruction.Create(OpCodes.Call, getRoot));
    il.Append(Instruction.Create(OpCodes.Callvirt, getTouchTarget));
    il.Append(Instruction.Create(OpCodes.Call, getRoot));
    il.Append(Instruction.Create(OpCodes.Bne_Un, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldfld, eventData));
    il.Append(Instruction.Create(OpCodes.Stloc, dataLocal));
    il.Append(Instruction.Create(OpCodes.Ldloc, dataLocal));
    il.Append(Instruction.Create(OpCodes.Isinst, uidragArgs));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldloc, dataLocal));
    il.Append(Instruction.Create(OpCodes.Unbox_Any, uidragArgs));
    il.Append(Instruction.Create(OpCodes.Stloc, argsLocal));
    il.Append(Instruction.Create(OpCodes.Ldloca, argsLocal));
    il.Append(Instruction.Create(OpCodes.Ldfld, uiType));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Bne_Un, done));
    il.Append(Instruction.Create(OpCodes.Ldloca, argsLocal));
    il.Append(Instruction.Create(OpCodes.Call, release));
    il.Append(Instruction.Create(OpCodes.Call, getItemComponent));
    il.Append(Instruction.Create(OpCodes.Callvirt, getItemDictionary));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, dragIndex));
    il.Append(Instruction.Create(OpCodes.Ldloca, itemLocal));
    il.Append(Instruction.Create(OpCodes.Callvirt, tryGetItem));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, zoneScene));
    il.Append(Instruction.Create(OpCodes.Callvirt, getSession));
    il.Append(Instruction.Create(OpCodes.Newobj, requestCtor));
    il.Append(Instruction.Create(OpCodes.Dup));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, dragIndex));
    il.Append(Instruction.Create(OpCodes.Callvirt, setIndex));
    il.Append(Instruction.Create(OpCodes.Dup));
    il.Append(Instruction.Create(OpCodes.Ldloc, itemLocal));
    il.Append(Instruction.Create(OpCodes.Ldfld, count));
    il.Append(Instruction.Create(OpCodes.Callvirt, setCount));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Call, callDelete));
    il.Append(Instruction.Create(OpCodes.Pop));
    il.Append(done);
}

static void PatchMeltingGemRefresh(ModuleDefinition module)
{
    var melting = FindType(module, "ET.MeltingUI");
    var fuiMelting = FindType(module, "ET.FUI_MeltingUI");
    var stateMachine = AllTypes(module).Single(type =>
        type.Name.Contains("<AwakeAsync>g__Melt|6", StringComparison.Ordinal)
        && type.Methods.Any(method => method.Name == "MoveNext"));
    var moveNext = stateMachine.Methods.Single(method => method.Name == "MoveNext");
    var stateThis = stateMachine.Fields.Single(field => field.Name == "<>4__this");
    var displayClass = stateThis.FieldType.Resolve()
        ?? throw new InvalidOperationException("MeltingUI display class not found");
    var displayThis = displayClass.Fields.Single(field => field.Name == "<>4__this");
    var gemIndex = displayClass.Fields.Single(field => field.Name == "gemIndex");
    var meltingUI = melting.Fields.Single(field => field.Name == "meltingUI");
    var btnGem = fuiMelting.Fields.Single(field => field.Name == "m_btnGem");
    var btnGemDefinition = btnGem.FieldType.Resolve()
        ?? throw new InvalidOperationException("MeltingUI gem button type not found");
    var self = btnGemDefinition.Fields.Single(field => field.Name == "self");
    var setIcon = FindMethodReference(module, "FairyGUI.GObject", "set_icon");
    var displayLocal = moveNext.Body.Variables.Single(variable =>
        variable.VariableType.FullName == displayClass.FullName);
    var publish = moveNext.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Callvirt
        && instruction.Operand is MethodReference method
        && method.Name == "Publish_Sync");

    // Idempotence for clients that were already patched by this version.
    if (FindMeltingGemRefreshSequence(module) != null)
        return;

    var il = moveNext.Body.GetILProcessor();
    var cursor = publish;
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldloc, displayLocal),
        Instruction.Create(OpCodes.Ldc_I4_M1),
        Instruction.Create(OpCodes.Stfld, gemIndex),
        Instruction.Create(OpCodes.Ldloc, displayLocal),
        Instruction.Create(OpCodes.Ldfld, displayThis),
        Instruction.Create(OpCodes.Ldfld, meltingUI),
        Instruction.Create(OpCodes.Ldfld, btnGem),
        Instruction.Create(OpCodes.Ldfld, self),
        Instruction.Create(OpCodes.Ldnull),
        Instruction.Create(OpCodes.Callvirt, setIcon),
    })
    {
        il.InsertAfter(cursor, instruction);
        cursor = instruction;
    }
}

static IReadOnlyList<Instruction>? FindMeltingGemRefreshSequence(ModuleDefinition module)
{
    var melting = FindType(module, "ET.MeltingUI");
    var fuiMelting = FindType(module, "ET.FUI_MeltingUI");
    var stateMachine = AllTypes(module).Single(type =>
        type.Name.Contains("<AwakeAsync>g__Melt|6", StringComparison.Ordinal)
        && type.Methods.Any(method => method.Name == "MoveNext"));
    var moveNext = stateMachine.Methods.Single(method => method.Name == "MoveNext");
    var stateThis = stateMachine.Fields.Single(field => field.Name == "<>4__this");
    var displayClass = stateThis.FieldType.Resolve()
        ?? throw new InvalidOperationException("MeltingUI display class not found");
    var displayThis = displayClass.Fields.Single(field => field.Name == "<>4__this");
    var gemIndex = displayClass.Fields.Single(field => field.Name == "gemIndex");
    var meltingUI = melting.Fields.Single(field => field.Name == "meltingUI");
    var btnGem = fuiMelting.Fields.Single(field => field.Name == "m_btnGem");
    var btnGemDefinition = btnGem.FieldType.Resolve()
        ?? throw new InvalidOperationException("MeltingUI gem button type not found");
    var self = btnGemDefinition.Fields.Single(field => field.Name == "self");
    var displayLocal = moveNext.Body.Variables.Single(variable =>
        variable.VariableType.FullName == displayClass.FullName);
    var publish = moveNext.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Callvirt
        && instruction.Operand is MethodReference method
        && method.Name == "Publish_Sync");
    var start = moveNext.Body.Instructions.IndexOf(publish) + 1;
    if (start < 1 || start + 10 > moveNext.Body.Instructions.Count)
        return null;

    var sequence = moveNext.Body.Instructions.Skip(start).Take(10).ToArray();
    if (sequence[0].OpCode != OpCodes.Ldloc || sequence[0].Operand != displayLocal
        || sequence[1].OpCode != OpCodes.Ldc_I4_M1
        || sequence[2].OpCode != OpCodes.Stfld || !IsField(sequence[2].Operand, gemIndex)
        || sequence[3].OpCode != OpCodes.Ldloc || sequence[3].Operand != displayLocal
        || sequence[4].OpCode != OpCodes.Ldfld || !IsField(sequence[4].Operand, displayThis)
        || sequence[5].OpCode != OpCodes.Ldfld || !IsField(sequence[5].Operand, meltingUI)
        || sequence[6].OpCode != OpCodes.Ldfld || !IsField(sequence[6].Operand, btnGem)
        || sequence[7].OpCode != OpCodes.Ldfld || !IsField(sequence[7].Operand, self)
        || sequence[8].OpCode != OpCodes.Ldnull
        || sequence[9].OpCode != OpCodes.Callvirt
        || sequence[9].Operand is not MethodReference setIcon || setIcon.Name != "set_icon")
    {
        return null;
    }
    return sequence;
}

static void RemoveMeltingGemRefresh(ModuleDefinition module)
{
    var sequence = FindMeltingGemRefreshSequence(module)
        ?? throw new InvalidOperationException("melting refresh patch sequence not found; refusing to modify the DLL");
    var stateMachine = AllTypes(module).Single(type =>
        type.Name.Contains("<AwakeAsync>g__Melt|6", StringComparison.Ordinal)
        && type.Methods.Any(method => method.Name == "MoveNext"));
    var il = stateMachine.Methods.Single(method => method.Name == "MoveNext").Body.GetILProcessor();
    foreach (var instruction in sequence)
        il.Remove(instruction);
}

static void ValidateMeltingGemRefresh(string path, IAssemblyResolver resolver, bool expected)
{
    using var module = ModuleDefinition.ReadModule(path, new ReaderParameters
    {
        InMemory = true,
        ReadSymbols = false,
        AssemblyResolver = resolver,
    });
    var installed = FindMeltingGemRefreshSequence(module) != null;
    if (installed != expected)
        throw new InvalidOperationException($"melting refresh validation failed: installed={installed}, expected={expected}");
}

static MethodReference FindMethodReference(ModuleDefinition module, string declaringType, string name)
{
    return module.GetMemberReferences().OfType<MethodReference>().First(method =>
        method.Name == name && method.DeclaringType.FullName == declaringType);
}

static FieldReference FindFieldReference(ModuleDefinition module, string declaringType, string name)
{
    return module.GetMemberReferences().OfType<FieldReference>().First(field =>
        field.Name == name && field.DeclaringType.FullName == declaringType);
}

static MethodReference FindUsedMethod(ModuleDefinition module, string name, string signatureNeedle)
{
    return AllTypes(module)
        .SelectMany(type => type.Methods)
        .Where(method => method.HasBody)
        .SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand)
        .OfType<MethodReference>()
        .First(method => method.Name == name && method.FullName.Contains(signatureNeedle, StringComparison.Ordinal));
}

static GenericInstanceMethod FindUsedGenericMethod(ModuleDefinition module, string name, string genericType)
{
    return AllTypes(module)
        .SelectMany(type => type.Methods)
        .Where(method => method.HasBody)
        .SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand)
        .OfType<GenericInstanceMethod>()
        .First(method => method.Name == name
            && method.GenericArguments.Any(argument => argument.FullName == genericType));
}

static TypeDefinition FindType(ModuleDefinition module, string fullName)
{
    return AllTypes(module).Single(type => type.FullName == fullName);
}

static TypeReference FindTypeReference(ModuleDefinition module, string fullName)
{
    return module.GetTypeReferences().Single(type => type.FullName == fullName);
}

static IEnumerable<TypeDefinition> AllTypes(ModuleDefinition module)
{
    foreach (var type in module.Types)
    {
        yield return type;
        foreach (var nested in AllNestedTypes(type))
            yield return nested;
    }
}

static IEnumerable<TypeDefinition> AllNestedTypes(TypeDefinition type)
{
    foreach (var nested in type.NestedTypes)
    {
        yield return nested;
        foreach (var child in AllNestedTypes(nested))
            yield return child;
    }
}

static bool IsField(object? operand, FieldDefinition expected)
{
    return operand is FieldReference field
        && field.Name == expected.Name
        && field.DeclaringType.FullName == expected.DeclaringType.FullName;
}

static void ValidatePatchedModule(string path, IAssemblyResolver resolver)
{
    using var module = ModuleDefinition.ReadModule(path, new ReaderParameters
    {
        InMemory = true,
        ReadSymbols = false,
        AssemblyResolver = resolver,
    });
    var mainUI = FindType(module, "ET.MainUI");
    var moveNext = mainUI.NestedTypes.Single(type => type.Name == "<AwakeAsync>d__3")
        .Methods.Single(method => method.Name == "MoveNext");

    string BindingTarget(string fieldName)
    {
        var instructions = moveNext.Body.Instructions;
        var fieldIndex = instructions.IndexOf(instructions.Single(instruction =>
            instruction.OpCode == OpCodes.Ldfld
            && instruction.Operand is FieldReference field
            && field.Name == fieldName));
        return instructions.Skip(fieldIndex + 1).Take(8)
            .Single(instruction => instruction.OpCode == OpCodes.Ldftn)
            .Operand is MethodReference method
            ? method.Name
            : string.Empty;
    }

    if (BindingTarget("m_btnRank") != "<AwakeAsync>b__3_4")
        throw new InvalidOperationException("ranking button binding changed unexpectedly");
    if (BindingTarget("m_btnLittleGame") != "Codex_OpenQuest")
        throw new InvalidOperationException("little game button was not bound to Quest1UI");
    if (BindingTarget("m_btnTeam") != "Codex_OpenTeam")
        throw new InvalidOperationException("team button was not bound to social UI");

    string HeadBindingTarget(string listenerName)
    {
        var instructions = moveNext.Body.Instructions;
        var headButtons = instructions.Where(instruction =>
            instruction.OpCode == OpCodes.Ldfld
            && instruction.Operand is FieldReference field
            && field.DeclaringType.FullName == "ET.FUI_HeadInfoItemMain"
            && field.Name == "m_btn");
        foreach (var headButton in headButtons)
        {
            var fieldIndex = instructions.IndexOf(headButton);
            var binding = instructions.Skip(fieldIndex + 1).Take(8).ToList();
            if (!binding.Any(instruction =>
                    instruction.Operand is MethodReference method && method.Name == listenerName))
                continue;
            return binding.Single(instruction => instruction.OpCode == OpCodes.Ldftn)
                .Operand is MethodReference callback
                ? callback.Name
                : string.Empty;
        }
        return string.Empty;
    }

    if (HeadBindingTarget("get_onClick") != "<AwakeAsync>b__3_10")
        throw new InvalidOperationException("level area left click was not bound to CharacterUI");
    if (HeadBindingTarget("get_onRightClick") != "<AwakeAsync>b__3_0")
        throw new InvalidOperationException("level area right click was not restored to the original buff refresh callback");

    var playEffect = FindType(module, "ET.PlayEffectEvent");
    var playBullet = playEffect.Methods.Single(method => method.Name == "PlayBulletEffect");
    var domove = playBullet.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference method
        && method.DeclaringType.FullName == "DG.Tweening.ShortcutExtensions"
        && method.Name == "DOMove").Operand as MethodReference
        ?? throw new InvalidOperationException("projectile DOMove call is missing");
    if (!domove.ReturnType.FullName.StartsWith("DG.Tweening.Core.TweenerCore`3", StringComparison.Ordinal))
        throw new InvalidOperationException($"projectile DOMove return type is still {domove.ReturnType.FullName}");

    var quest = mainUI.Methods.Single(method => method.Name == "Codex_OpenQuest");
    if (!quest.Body.Variables.Any(variable => variable.VariableType.Name == "Quest_1_Open")
        || !quest.Body.Instructions.Any(instruction =>
            instruction.Operand is FieldReference field && field.Name == "cardCount")
        || !quest.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Ldc_I4_S
            && Convert.ToInt32(instruction.Operand) == 10))
    {
        throw new InvalidOperationException("Quest1UI publish body is incomplete");
    }

    var bagUI = FindType(module, "ET.BagUI");
    var discard = bagUI.Methods.SingleOrDefault(method => method.Name == "Codex_DiscardItem")
        ?? throw new InvalidOperationException("bag discard callback is missing");
    if (!discard.Body.Instructions.Any(instruction =>
            instruction.Operand is MethodReference method
            && method.DeclaringType.FullName == "ET.C2M_DeleteItem")
        || !discard.Body.Instructions.Any(instruction =>
            instruction.Operand is MethodReference method
            && method.DeclaringType.FullName == "FairyGUI.EventContext"
            && method.Name == "get_initiator")
        || !discard.Body.Instructions.Any(instruction =>
            instruction.Operand is MethodReference method
            && method.DeclaringType.FullName == "FairyGUI.GRoot"
            && method.Name == "get_touchTarget")
        || !discard.Body.Instructions.Any(instruction =>
            instruction.Operand is FieldReference field
            && field.DeclaringType.FullName == "ET.ClientItemData"
            && field.Name == "Count"))
    {
        throw new InvalidOperationException("bag discard callback does not send the full stack");
    }

    var bagAwake = bagUI.NestedTypes.Single(type => type.Name == "<AwakeAsync>d__14")
        .Methods.Single(method => method.Name == "MoveNext");
    if (!bagAwake.Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Ldftn
            && instruction.Operand is MethodReference method
            && method.Name == discard.Name)
        || !bagUI.Methods.Single(method => method.Name == "Destroy").Body.Instructions.Any(instruction =>
            instruction.Operand is MethodReference method
            && method.DeclaringType.FullName == "FairyGUI.EventListener"
            && method.Name == "Remove1"))
    {
        throw new InvalidOperationException("bag discard root listener lifecycle is incomplete");
    }

    var meltStateMachine = AllTypes(module).Single(type =>
        type.Name.Contains("<AwakeAsync>g__Melt|6", StringComparison.Ordinal)
        && type.Methods.Any(method => method.Name == "MoveNext"));
    if (!meltStateMachine.Methods.Single(method => method.Name == "MoveNext").Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Stfld
            && instruction.Operand is FieldReference field
            && field.Name == "gemIndex"))
    {
        throw new InvalidOperationException("melting success does not clear the consumed gem selection");
    }

}

static void CloneBody(ModuleDefinition module, MethodDefinition source, MethodDefinition destination)
{
    if (!source.HasBody)
        throw new InvalidOperationException($"method {source.FullName} has no body");

    destination.Body.InitLocals = source.Body.InitLocals;
    var variableMap = new Dictionary<VariableDefinition, VariableDefinition>();
    foreach (var sourceVariable in source.Body.Variables)
    {
        var destinationVariable = new VariableDefinition(module.ImportReference(sourceVariable.VariableType));
        destination.Body.Variables.Add(destinationVariable);
        variableMap[sourceVariable] = destinationVariable;
    }

    var instructionMap = new Dictionary<Instruction, Instruction>();
    foreach (var sourceInstruction in source.Body.Instructions)
    {
        var destinationInstruction = CloneInstruction(module, sourceInstruction, variableMap);
        destination.Body.Instructions.Add(destinationInstruction);
        instructionMap[sourceInstruction] = destinationInstruction;
    }

    foreach (var sourceInstruction in source.Body.Instructions)
    {
        var destinationInstruction = instructionMap[sourceInstruction];
        if (sourceInstruction.Operand is Instruction branch)
            destinationInstruction.Operand = instructionMap[branch];
        else if (sourceInstruction.Operand is Instruction[] branches)
            destinationInstruction.Operand = branches.Select(item => instructionMap[item]).ToArray();
    }
}

static Instruction CloneInstruction(
    ModuleDefinition module,
    Instruction source,
    IReadOnlyDictionary<VariableDefinition, VariableDefinition> variables)
{
    return source.Operand switch
    {
        null => Instruction.Create(source.OpCode),
        sbyte value => Instruction.Create(source.OpCode, value),
        byte value => Instruction.Create(source.OpCode, (sbyte)value),
        int value => Instruction.Create(source.OpCode, value),
        long value => Instruction.Create(source.OpCode, value),
        float value => Instruction.Create(source.OpCode, value),
        double value => Instruction.Create(source.OpCode, value),
        string value => Instruction.Create(source.OpCode, value),
        VariableDefinition variable => Instruction.Create(source.OpCode, variables[variable]),
        ParameterDefinition parameter => Instruction.Create(source.OpCode, parameter),
        MethodReference method => Instruction.Create(source.OpCode, module.ImportReference(method)),
        FieldReference field => Instruction.Create(source.OpCode, module.ImportReference(field)),
        TypeReference type => Instruction.Create(source.OpCode, module.ImportReference(type)),
        Instruction => Instruction.Create(source.OpCode, Instruction.Create(OpCodes.Nop)),
        Instruction[] => Instruction.Create(source.OpCode, Array.Empty<Instruction>()),
        _ => throw new NotSupportedException($"unsupported operand {source.Operand.GetType().FullName}"),
    };
}

static void ConvertRankingPublishToQuest(ModuleDefinition module, MethodDefinition method)
{
    var rankingType = module.GetTypeReferences().Single(type => type.Name == "RankingUI_Open");
    var questType = module.GetTypeReferences().Single(type => type.Name == "Quest_1_Open");
    var questFields = module.GetMemberReferences()
        .OfType<FieldReference>()
        .Where(field => field.DeclaringType.FullName == questType.FullName)
        .ToList();
    var questZoneScene = questFields.Single(field => field.Name == "zoneScene");
    var questCardCount = questFields.Single(field => field.Name == "cardCount");

    foreach (var variable in method.Body.Variables)
    {
        if (variable.VariableType.FullName == rankingType.FullName)
            variable.VariableType = module.ImportReference(questType);
    }

    foreach (var instruction in method.Body.Instructions)
    {
        if (instruction.Operand is TypeReference type && type.FullName == rankingType.FullName)
        {
            instruction.Operand = questType;
            continue;
        }

        if (instruction.Operand is FieldReference field
            && field.DeclaringType.FullName == rankingType.FullName
            && field.Name == "zoneScene")
        {
            instruction.Operand = questZoneScene;
            continue;
        }

        if (instruction.Operand is GenericInstanceMethod generic
            && generic.GenericArguments.Any(argument => argument.FullName == rankingType.FullName))
        {
            var replacement = new GenericInstanceMethod(module.ImportReference(generic.ElementMethod));
            replacement.GenericArguments.Add(questType);
            instruction.Operand = replacement;
        }
    }

    var zoneStore = method.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Stfld
        && instruction.Operand is FieldReference field
        && field.DeclaringType.FullName == questType.FullName
        && field.Name == "zoneScene");
    var il = method.Body.GetILProcessor();
    var cursor = zoneStore;
    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldloca_S, method.Body.Variables[0]),
        Instruction.Create(OpCodes.Ldc_I4_S, (sbyte)10),
        Instruction.Create(OpCodes.Stfld, questCardCount),
    })
    {
        il.InsertAfter(cursor, instruction);
        cursor = instruction;
    }
}

sealed class GameAssemblyResolver : DefaultAssemblyResolver
{
    private readonly string inputDirectory;

    public GameAssemblyResolver(string inputDirectory)
    {
        this.inputDirectory = inputDirectory;
    }

    public override AssemblyDefinition Resolve(AssemblyNameReference name)
    {
        return Resolve(name, new ReaderParameters { AssemblyResolver = this });
    }

    public override AssemblyDefinition Resolve(AssemblyNameReference name, ReaderParameters parameters)
    {
        var mappedName = name.Name switch
        {
            "Unity.Hotfix" => "Hotfix.dll",
            "Unity.HotfixView" => "HotfixView.dll",
            _ => null,
        };
        if (mappedName != null)
        {
            var path = Path.Combine(inputDirectory, mappedName);
            if (File.Exists(path))
            {
                parameters.AssemblyResolver = this;
                parameters.InMemory = true;
                parameters.ReadSymbols = false;
                return AssemblyDefinition.ReadAssembly(path, parameters);
            }
        }
        return base.Resolve(name, parameters);
    }
}
