using Mono.Cecil;
using Mono.Cecil.Cil;

const string DirectLoginHelperName = "CodexLauncherDirectLogin";
const string AutoEnterHelperName = "CodexLauncherAutoEnter";
const string AutoEnterEnvironmentVariable = "MHQ_LAUNCHER_AUTO_ENTER";

if (args.Length != 2)
    throw new ArgumentException("usage: ClientLauncherAutoEnterPatcher <input HotfixView.dll> <output HotfixView.dll>");

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
});

var loginSystem = module.Types.Single(type => type.FullName == "ET.FUI_LoginStartSystem");
var loginClosure = loginSystem.NestedTypes.Single(type => type.Name == "<>c__DisplayClass0_0");
if (loginClosure.Methods.All(method => method.Name != DirectLoginHelperName))
    throw new InvalidOperationException("install the launcher direct-login patch before auto-enter");

var enterSystem = module.Types.Single(type => type.FullName == "ET.FUI_EnterGameStartSystem");
var enterView = module.Types.Single(type => type.FullName == "ET.FUI_EnterGame");
var closure = enterSystem.NestedTypes.Single(type => type.Name == "<>c__DisplayClass2_0");
var stateMachine = enterSystem.NestedTypes.Single(type => type.Name == "<Start>d__2");
var moveNext = stateMachine.Methods.Single(method => method.Name == "MoveNext");
var existingHelper = enterSystem.Methods.FirstOrDefault(method => method.Name == AutoEnterHelperName);
if (existingHelper is not null)
{
    if (!moveNext.Body.Instructions.Any(instruction =>
            instruction.Operand is MethodReference method && method.Name == AutoEnterHelperName))
        throw new InvalidOperationException("launcher auto-enter helper exists but EnterGame Start does not call it");
    File.Copy(input, output, true);
    Console.WriteLine("launcher auto-enter patch already installed");
    return;
}

var callback = closure.Methods.Single(method => method.Name == "<Start>b__0" && method.Parameters.Count == 0);
var closureConstructor = closure.Methods.Single(method => method.IsConstructor && !method.IsStatic);
var closureSystemField = closure.Fields.Single(field => field.Name == "<>4__this");
var closureSelfField = closure.Fields.Single(field => field.Name == "self");
var closureZoneSceneField = closure.Fields.Single(field => field.Name == "zoneScene");
var stateSystemField = stateMachine.Fields.Single(field => field.Name == "<>4__this");
var stateSelfField = stateMachine.Fields.Single(field => field.Name == "self");
var zoneScene = callback.Body.Instructions
    .Select(instruction => instruction.Operand)
    .OfType<MethodReference>()
    .Single(method => method.Name == "ZoneScene");

var helper = AddHelper(module, enterSystem, enterView, closure, closureConstructor, callback,
    closureSystemField, closureSelfField, closureZoneSceneField, zoneScene);
PatchMoveNext(module, moveNext, helper, stateSystemField, stateSelfField);

module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine("installed launcher auto-enter patch");

static MethodDefinition AddHelper(
    ModuleDefinition module,
    TypeDefinition enterSystem,
    TypeDefinition enterView,
    TypeDefinition closure,
    MethodDefinition closureConstructor,
    MethodDefinition callback,
    FieldDefinition closureSystemField,
    FieldDefinition closureSelfField,
    FieldDefinition closureZoneSceneField,
    MethodReference zoneScene)
{
    var helper = new MethodDefinition(
        AutoEnterHelperName,
        MethodAttributes.Public | MethodAttributes.Static | MethodAttributes.HideBySig,
        module.TypeSystem.Void);
    helper.Parameters.Add(new ParameterDefinition("system", ParameterAttributes.None, enterSystem));
    helper.Parameters.Add(new ParameterDefinition("self", ParameterAttributes.None, enterView));
    enterSystem.Methods.Add(helper);
    helper.Body.InitLocals = true;

    var flag = new VariableDefinition(module.TypeSystem.String);
    var callbackTarget = new VariableDefinition(closure);
    helper.Body.Variables.Add(flag);
    helper.Body.Variables.Add(callbackTarget);

    var environment = new TypeReference(
        "System", "Environment", module, module.TypeSystem.Object.Scope);
    var getEnvironmentVariable = new MethodReference(
        "GetEnvironmentVariable", module.TypeSystem.String, environment)
    {
        HasThis = false,
    };
    getEnvironmentVariable.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    var setEnvironmentVariable = new MethodReference(
        "SetEnvironmentVariable", module.TypeSystem.Void, environment)
    {
        HasThis = false,
    };
    setEnvironmentVariable.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    setEnvironmentVariable.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    var stringEquals = new MethodReference(
        "Equals", module.TypeSystem.Boolean, module.TypeSystem.String)
    {
        HasThis = false,
    };
    stringEquals.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    stringEquals.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));

    var done = Instruction.Create(OpCodes.Ret);
    var il = helper.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldstr, AutoEnterEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Call, getEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Stloc, flag));
    il.Append(Instruction.Create(OpCodes.Ldstr, AutoEnterEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Ldnull));
    il.Append(Instruction.Create(OpCodes.Call, setEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Ldloc, flag));
    il.Append(Instruction.Create(OpCodes.Ldstr, "1"));
    il.Append(Instruction.Create(OpCodes.Call, stringEquals));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));

    il.Append(Instruction.Create(OpCodes.Newobj, closureConstructor));
    il.Append(Instruction.Create(OpCodes.Stloc, callbackTarget));
    il.Append(Instruction.Create(OpCodes.Ldloc, callbackTarget));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Stfld, closureSystemField));
    il.Append(Instruction.Create(OpCodes.Ldloc, callbackTarget));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Stfld, closureSelfField));
    il.Append(Instruction.Create(OpCodes.Ldloc, callbackTarget));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(zoneScene)));
    il.Append(Instruction.Create(OpCodes.Stfld, closureZoneSceneField));
    il.Append(Instruction.Create(OpCodes.Ldloc, callbackTarget));
    il.Append(Instruction.Create(OpCodes.Call, callback));
    il.Append(done);
    return helper;
}

static void PatchMoveNext(
    ModuleDefinition module,
    MethodDefinition moveNext,
    MethodDefinition helper,
    FieldDefinition stateSystemField,
    FieldDefinition stateSelfField)
{
    var setPosition = moveNext.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Callvirt
        && instruction.Operand is MethodReference method
        && method.Name == "set_position"
        && method.DeclaringType.FullName == "UnityEngine.Transform");
    var insertionPoint = setPosition.Next;
    var il = moveNext.Body.GetILProcessor();
    il.InsertBefore(insertionPoint, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(insertionPoint, Instruction.Create(OpCodes.Ldfld, stateSystemField));
    il.InsertBefore(insertionPoint, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(insertionPoint, Instruction.Create(OpCodes.Ldfld, stateSelfField));
    il.InsertBefore(insertionPoint, Instruction.Create(OpCodes.Call, module.ImportReference(helper)));
}
