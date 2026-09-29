using Mono.Cecil;
using Mono.Cecil.Cil;

const string DialogText = "你的账号已在别的地方登录，当前登录已失效，已与服务器断开连接。点击确定退出游戏。";

if (args.Length != 2)
{
    Console.Error.WriteLine("usage: ClientForceOfflinePatcher <input-dll> <output-dll>");
    return 2;
}

var inputPath = Path.GetFullPath(args[0]);
var outputPath = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(inputPath, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
});

Patch(module);
Directory.CreateDirectory(Path.GetDirectoryName(outputPath)!);
module.Write(outputPath);
Validate(outputPath);
Console.WriteLine("verified G2C_ForceOffLine: confirmation dialog -> original Quit event");
return 0;

static void Patch(ModuleDefinition module)
{
    var handler = FindType(module, "ET.G2C_ForceOffLineHandler");
    var showDialog = handler.Methods.SingleOrDefault(method => method.Name == "Codex_ShowForceOfflineDialog");
    var quitAfterConfirm = handler.Methods.SingleOrDefault(method => method.Name == "Codex_QuitAfterConfirm");
    if (showDialog != null || quitAfterConfirm != null)
    {
        if (showDialog == null || quitAfterConfirm == null)
            throw new InvalidOperationException("incomplete force-offline patch already exists");
        return;
    }

    var stateMachine = handler.NestedTypes.Single(type => type.Name == "<Run>d__0");
    var moveNext = stateMachine.Methods.Single(method => method.Name == "MoveNext");
    var originalPublish = moveNext.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Callvirt
        && instruction.Operand is GenericInstanceMethod method
        && method.Name == "Publish_Sync"
        && method.GenericArguments.Any(type => type.FullName == "ET.EventType.Quit"));
    var gameEventSystem = moveNext.Body.Instructions
        .Take(moveNext.Body.Instructions.IndexOf(originalPublish))
        .Last(instruction => instruction.OpCode == OpCodes.Call
            && instruction.Operand is MethodReference method
            && method.Name == "get_EventSystem"
            && method.DeclaringType.FullName == "ET.Game");

    var quitType = FindType(module, "ET.EventType.Quit");
    var showTipType = FindType(module, "ET.EventType.ShowTipUI");
    var tipField = showTipType.Fields.Single(field => field.Name == "tip");
    var tipTypeField = showTipType.Fields.Single(field => field.Name == "tipType");
    var okActionField = showTipType.Fields.Single(field => field.Name == "okAction");
    var publishShowTip = FindUsedGenericMethod(module, "Publish", showTipType.FullName);
    var coroutine = FindUsedMethod(module, "ET.ETTask", "Coroutine");

    quitAfterConfirm = new MethodDefinition(
        "Codex_QuitAfterConfirm",
        MethodAttributes.Private | MethodAttributes.Static | MethodAttributes.HideBySig,
        module.TypeSystem.Void);
    quitAfterConfirm.Parameters.Add(new ParameterDefinition("_", ParameterAttributes.None, module.TypeSystem.String));
    quitAfterConfirm.Body.InitLocals = true;
    var quitLocal = new VariableDefinition(quitType);
    quitAfterConfirm.Body.Variables.Add(quitLocal);
    var quitIL = quitAfterConfirm.Body.GetILProcessor();
    quitIL.Append(Instruction.Create(OpCodes.Call, (MethodReference)gameEventSystem.Operand));
    quitIL.Append(Instruction.Create(OpCodes.Ldloca_S, quitLocal));
    quitIL.Append(Instruction.Create(OpCodes.Initobj, quitType));
    quitIL.Append(Instruction.Create(OpCodes.Ldloc, quitLocal));
    quitIL.Append(Instruction.Create(OpCodes.Callvirt, (MethodReference)originalPublish.Operand));
    quitIL.Append(Instruction.Create(OpCodes.Ret));
    handler.Methods.Add(quitAfterConfirm);

    showDialog = new MethodDefinition(
        "Codex_ShowForceOfflineDialog",
        MethodAttributes.Private | MethodAttributes.Static | MethodAttributes.HideBySig,
        module.TypeSystem.Void);
    showDialog.Body.InitLocals = true;
    var tipLocal = new VariableDefinition(showTipType);
    var publishTaskLocal = new VariableDefinition(module.ImportReference(publishShowTip.ReturnType));
    showDialog.Body.Variables.Add(tipLocal);
    showDialog.Body.Variables.Add(publishTaskLocal);
    var actionConstructor = new MethodReference(
        ".ctor",
        module.TypeSystem.Void,
        module.ImportReference(okActionField.FieldType))
    {
        HasThis = true,
    };
    actionConstructor.Parameters.Add(new ParameterDefinition(module.TypeSystem.Object));
    actionConstructor.Parameters.Add(new ParameterDefinition(module.TypeSystem.IntPtr));

    var showIL = showDialog.Body.GetILProcessor();
    showIL.Append(Instruction.Create(OpCodes.Ldloca_S, tipLocal));
    showIL.Append(Instruction.Create(OpCodes.Initobj, showTipType));
    showIL.Append(Instruction.Create(OpCodes.Ldloca_S, tipLocal));
    showIL.Append(Instruction.Create(OpCodes.Ldstr, DialogText));
    showIL.Append(Instruction.Create(OpCodes.Stfld, tipField));
    showIL.Append(Instruction.Create(OpCodes.Ldloca_S, tipLocal));
    showIL.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    showIL.Append(Instruction.Create(OpCodes.Stfld, tipTypeField));
    showIL.Append(Instruction.Create(OpCodes.Ldloca_S, tipLocal));
    showIL.Append(Instruction.Create(OpCodes.Ldnull));
    showIL.Append(Instruction.Create(OpCodes.Ldftn, quitAfterConfirm));
    showIL.Append(Instruction.Create(OpCodes.Newobj, actionConstructor));
    showIL.Append(Instruction.Create(OpCodes.Stfld, okActionField));
    showIL.Append(Instruction.Create(OpCodes.Call, (MethodReference)gameEventSystem.Operand));
    showIL.Append(Instruction.Create(OpCodes.Ldloc, tipLocal));
    showIL.Append(Instruction.Create(OpCodes.Callvirt, publishShowTip));
    showIL.Append(Instruction.Create(OpCodes.Stloc, publishTaskLocal));
    showIL.Append(Instruction.Create(OpCodes.Ldloca_S, publishTaskLocal));
    showIL.Append(Instruction.Create(OpCodes.Call, coroutine));
    showIL.Append(Instruction.Create(OpCodes.Ret));
    handler.Methods.Add(showDialog);

    var instructions = moveNext.Body.Instructions;
    var start = instructions.IndexOf(gameEventSystem);
    var end = instructions.IndexOf(originalPublish);
    if (end - start != 4)
        throw new InvalidOperationException("unexpected original force-offline handler shape");
    gameEventSystem.OpCode = OpCodes.Call;
    gameEventSystem.Operand = showDialog;
    for (var index = start + 1; index <= end; index++)
    {
        instructions[index].OpCode = OpCodes.Nop;
        instructions[index].Operand = null;
    }
}

static void Validate(string path)
{
    using var module = ModuleDefinition.ReadModule(path, new ReaderParameters
    {
        InMemory = true,
        ReadSymbols = false,
    });
    var handler = FindType(module, "ET.G2C_ForceOffLineHandler");
    var moveNext = handler.NestedTypes.Single(type => type.Name == "<Run>d__0")
        .Methods.Single(method => method.Name == "MoveNext");
    var showDialog = handler.Methods.Single(method => method.Name == "Codex_ShowForceOfflineDialog");
    var quitAfterConfirm = handler.Methods.Single(method => method.Name == "Codex_QuitAfterConfirm");

    if (!moveNext.Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Call
            && instruction.Operand is MethodReference method
            && method.Name == showDialog.Name))
        throw new InvalidOperationException("ForceOffLine handler does not open the confirmation dialog");
    if (moveNext.Body.Instructions.Any(instruction =>
            instruction.Operand is GenericInstanceMethod method
            && method.Name == "Publish_Sync"
            && method.GenericArguments.Any(type => type.FullName == "ET.EventType.Quit")))
        throw new InvalidOperationException("ForceOffLine handler still exits before confirmation");
    if (!showDialog.Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Ldstr && Equals(instruction.Operand, DialogText))
        || !showDialog.Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Ldftn
            && instruction.Operand is MethodReference method
            && method.Name == quitAfterConfirm.Name))
        throw new InvalidOperationException("confirmation text or callback is missing");
    var coroutineCall = showDialog.Body.Instructions.Single(instruction =>
        instruction.Operand is MethodReference method
        && method.DeclaringType.FullName == "ET.ETTask"
        && method.Name == "Coroutine");
    var coroutineIndex = showDialog.Body.Instructions.IndexOf(coroutineCall);
    if (coroutineIndex < 2
        || showDialog.Body.Instructions[coroutineIndex - 1].OpCode != OpCodes.Ldloca_S
        || showDialog.Body.Instructions[coroutineIndex - 2].OpCode != OpCodes.Stloc)
        throw new InvalidOperationException("ETTask.Coroutine must receive the address of a stored task value");
    if (!quitAfterConfirm.Body.Instructions.Any(instruction =>
            instruction.Operand is GenericInstanceMethod method
            && method.Name == "Publish_Sync"
            && method.GenericArguments.Any(type => type.FullName == "ET.EventType.Quit")))
        throw new InvalidOperationException("confirm callback does not publish the original Quit event");
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

static MethodReference FindUsedMethod(ModuleDefinition module, string declaringType, string name)
{
    return AllTypes(module)
        .SelectMany(type => type.Methods)
        .Where(method => method.HasBody)
        .SelectMany(method => method.Body.Instructions)
        .Select(instruction => instruction.Operand)
        .OfType<MethodReference>()
        .First(method => method.Name == name && method.DeclaringType.FullName == declaringType);
}

static TypeDefinition FindType(ModuleDefinition module, string fullName)
{
    return AllTypes(module).Single(type => type.FullName == fullName);
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
