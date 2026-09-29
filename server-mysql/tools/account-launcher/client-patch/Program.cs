using Mono.Cecil;
using Mono.Cecil.Cil;

const string HelperName = "CodexLauncherDirectLogin";
const string AccountEnvironmentVariable = "MHQ_LAUNCHER_ACCOUNT";
const string PasswordEnvironmentVariable = "MHQ_LAUNCHER_PASSWORD";

if (args.Length != 2)
    throw new ArgumentException("usage: ClientLauncherLoginPatcher <input HotfixView.dll> <output HotfixView.dll>");

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
});

var loginSystem = module.Types.Single(type => type.FullName == "ET.FUI_LoginStartSystem");
var start = loginSystem.Methods.Single(method => method.Name == "Start");
var closure = loginSystem.NestedTypes.Single(type => type.Name == "<>c__DisplayClass0_0");
var existingHelper = closure.Methods.FirstOrDefault(method => method.Name == HelperName);
if (existingHelper is not null)
{
    if (!start.Body.Instructions.Any(instruction =>
            instruction.Operand is MethodReference method && method.Name == HelperName))
        throw new InvalidOperationException("launcher login helper exists but Start does not call it");
    File.Copy(input, output, true);
    Console.WriteLine("launcher direct-login patch already installed");
    return;
}

var selfField = closure.Fields.Single(field => field.Name == "self");
var loginTypeField = closure.Fields.Single(field => field.Name == "loginType");
var loginCallback = closure.Methods.Single(method => method.Name == "<Start>b__0" && method.Parameters.Count == 0);
var loginView = module.Types.Single(type => type.FullName == "ET.FUI_Login");
var accountInputField = loginView.Fields.Single(field => field.Name == "m_iptAcc");
var passwordInputField = loginView.Fields.Single(field => field.Name == "m_iptPsd");
var setText = start.Body.Instructions
    .Select(instruction => instruction.Operand)
    .OfType<MethodReference>()
    .First(method => method.Name == "set_text" && method.DeclaringType.FullName == "FairyGUI.GObject");

var helper = AddHelper(module, closure, selfField, loginTypeField, loginCallback,
    accountInputField, passwordInputField, setText);
PatchStart(start, closure, helper);

module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine("installed launcher direct-login patch");

static MethodDefinition AddHelper(
    ModuleDefinition module,
    TypeDefinition closure,
    FieldDefinition selfField,
    FieldDefinition loginTypeField,
    MethodDefinition loginCallback,
    FieldDefinition accountInputField,
    FieldDefinition passwordInputField,
    MethodReference setText)
{
    var helper = new MethodDefinition(
        HelperName,
        MethodAttributes.Public | MethodAttributes.HideBySig,
        module.TypeSystem.Void);
    closure.Methods.Add(helper);
    helper.Body.InitLocals = true;

    var account = new VariableDefinition(module.TypeSystem.String);
    var password = new VariableDefinition(module.TypeSystem.String);
    helper.Body.Variables.Add(account);
    helper.Body.Variables.Add(password);

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
    var isNullOrEmpty = new MethodReference(
        "IsNullOrEmpty", module.TypeSystem.Boolean, module.TypeSystem.String)
    {
        HasThis = false,
    };
    isNullOrEmpty.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));

    var done = Instruction.Create(OpCodes.Ret);
    var il = helper.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldstr, AccountEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Call, getEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Stloc, account));
    il.Append(Instruction.Create(OpCodes.Ldstr, PasswordEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Call, getEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Stloc, password));

    il.Append(Instruction.Create(OpCodes.Ldstr, AccountEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Ldnull));
    il.Append(Instruction.Create(OpCodes.Call, setEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Ldstr, PasswordEnvironmentVariable));
    il.Append(Instruction.Create(OpCodes.Ldnull));
    il.Append(Instruction.Create(OpCodes.Call, setEnvironmentVariable));

    il.Append(Instruction.Create(OpCodes.Ldloc, account));
    il.Append(Instruction.Create(OpCodes.Call, isNullOrEmpty));
    il.Append(Instruction.Create(OpCodes.Brtrue, done));
    il.Append(Instruction.Create(OpCodes.Ldloc, password));
    il.Append(Instruction.Create(OpCodes.Call, isNullOrEmpty));
    il.Append(Instruction.Create(OpCodes.Brtrue, done));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, selfField));
    il.Append(Instruction.Create(OpCodes.Ldfld, accountInputField));
    il.Append(Instruction.Create(OpCodes.Ldloc, account));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setText)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, selfField));
    il.Append(Instruction.Create(OpCodes.Ldfld, passwordInputField));
    il.Append(Instruction.Create(OpCodes.Ldloc, password));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setText)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stfld, loginTypeField));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Call, loginCallback));
    il.Append(done);
    return helper;
}

static void PatchStart(MethodDefinition start, TypeDefinition closure, MethodDefinition helper)
{
    var closureLocal = start.Body.Variables.Single(variable => variable.VariableType.FullName == closure.FullName);
    var finalReturn = start.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Ret);
    var loadClosure = Instruction.Create(OpCodes.Ldloc, closureLocal);
    var callHelper = Instruction.Create(OpCodes.Callvirt, helper);
    var il = start.Body.GetILProcessor();
    il.InsertBefore(finalReturn, loadClosure);
    il.InsertBefore(finalReturn, callHelper);

    foreach (var instruction in start.Body.Instructions)
    {
        if (instruction == loadClosure || instruction == callHelper)
            continue;
        if (instruction.Operand == finalReturn)
            instruction.Operand = loadClosure;
        else if (instruction.Operand is Instruction[] targets)
        {
            for (var index = 0; index < targets.Length; index++)
                if (targets[index] == finalReturn)
                    targets[index] = loadClosure;
        }
    }
}
