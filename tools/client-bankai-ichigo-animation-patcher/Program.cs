using Mono.Cecil;
using Mono.Cecil.Cil;

const string RunClipName = "SkinBankaiIchigo_Run";
const string StateFieldName = "_BankaiIchigoRunState";
const string LoopMethodName = "LoopBankaiIchigoRun";

if (args.Length != 2)
{
    throw new ArgumentException(
        "usage: ClientBankaiIchigoAnimationPatcher " +
        "<input Unity.ModelView.dll> <output Unity.ModelView.dll>");
}

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
    AssemblyResolver = new NoResolveAssemblyResolver(),
});

var monoAnimancer = module.Types.Single(type => type.FullName == "ET.MonoAnimancer");
var playRun = monoAnimancer.Methods.Single(method => method.Name == "PlayRun");
var changed = RemoveLegacyBankaiLoop(playRun);

var stateField = monoAnimancer.Fields.SingleOrDefault(field => field.Name == StateFieldName);
if (stateField is not null)
{
    monoAnimancer.Fields.Remove(stateField);
    changed = true;
}
var loopMethod = monoAnimancer.Methods.SingleOrDefault(method => method.Name == LoopMethodName);
if (loopMethod is not null)
{
    monoAnimancer.Methods.Remove(loopMethod);
    changed = true;
}
if (AddPersistentBankaiGuard(playRun))
    changed = true;

Verify(monoAnimancer);
Directory.CreateDirectory(Path.GetDirectoryName(output)!);
if (changed)
    module.Write(output, new WriterParameters { WriteSymbols = false });
else
    File.Copy(input, output, true);

using var check = ModuleDefinition.ReadModule(output, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
    AssemblyResolver = new NoResolveAssemblyResolver(),
});
Verify(check.Types.Single(type => type.FullName == "ET.MonoAnimancer"));
Console.WriteLine(
    "patched Bankai Run like Byakuya: persistent guard with no time-jump callback");

static bool RemoveLegacyBankaiLoop(MethodDefinition playRun)
{
    var bankaiName = playRun.Body.Instructions.SingleOrDefault(instruction =>
        instruction.OpCode == OpCodes.Ldstr && (string?)instruction.Operand == RunClipName);
    if (bankaiName is null)
        return false;

    var start = bankaiName.Previous?.Previous?.Previous;
    var branchToContinuation = bankaiName.Next?.Next;
    if (start?.OpCode != OpCodes.Ldarg_0
        || start.Next?.OpCode != OpCodes.Ldfld
        || start.Next.Next?.OpCode != OpCodes.Callvirt
        || bankaiName.Next?.OpCode != OpCodes.Call
        || branchToContinuation?.OpCode != OpCodes.Brfalse
        || branchToContinuation.Operand is not Instruction continuation
        || continuation.OpCode != OpCodes.Ldarg_0)
    {
        throw new InvalidOperationException(
            "MonoAnimancer.PlayRun legacy Bankai branch was not recognized");
    }

    var cursor = start;
    var il = playRun.Body.GetILProcessor();
    while (cursor != continuation)
    {
        var next = cursor.Next
            ?? throw new InvalidOperationException("legacy Bankai branch has no continuation");
        il.Remove(cursor);
        cursor = next;
    }
    return true;
}

static bool AddPersistentBankaiGuard(MethodDefinition playRun)
{
    if (playRun.Body.Instructions.Any(instruction =>
            instruction.OpCode == OpCodes.Ldstr && (string?)instruction.Operand == RunClipName))
    {
        return false;
    }

    var skin25 = playRun.Body.Instructions.Single(instruction =>
        instruction.OpCode == OpCodes.Ldstr && (string?)instruction.Operand == "Skin25_Run");
    var loadThis = skin25.Previous?.Previous?.Previous;
    var loadClip = skin25.Previous?.Previous;
    var getName = skin25.Previous;
    var equality = skin25.Next;
    var skipCallback = equality?.Next;
    if (loadThis?.OpCode != OpCodes.Ldarg_0
        || loadClip?.OpCode != OpCodes.Ldfld
        || getName?.OpCode != OpCodes.Callvirt
        || equality?.OpCode != OpCodes.Call
        || (skipCallback?.OpCode != OpCodes.Brtrue && skipCallback?.OpCode != OpCodes.Brtrue_S)
        || skipCallback.Operand is not Instruction returnState)
    {
        throw new InvalidOperationException(
            "MonoAnimancer.PlayRun Skin25 persistent guard was not recognized");
    }

    var il = playRun.Body.GetILProcessor();
    il.InsertBefore(loadThis, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(loadThis, Instruction.Create(OpCodes.Ldfld, (FieldReference)loadClip.Operand));
    il.InsertBefore(loadThis, Instruction.Create(OpCodes.Callvirt, (MethodReference)getName.Operand));
    il.InsertBefore(loadThis, Instruction.Create(OpCodes.Ldstr, RunClipName));
    il.InsertBefore(loadThis, Instruction.Create(OpCodes.Call, (MethodReference)equality.Operand));
    il.InsertBefore(loadThis, Instruction.Create(OpCodes.Brtrue, returnState));
    return true;
}

static void Verify(TypeDefinition monoAnimancer)
{
    var playRun = monoAnimancer.Methods.Single(method => method.Name == "PlayRun");
    var guards = playRun.Body.Instructions
        .Where(instruction => instruction.OpCode == OpCodes.Ldstr)
        .Select(instruction => instruction.Operand as string)
        .Where(value => value is "Skin25_Run" or "SkinByakuya_Run" or RunClipName)
        .ToList();
    if (guards.Count(value => value == "Skin25_Run") != 1
        || guards.Count(value => value == "SkinByakuya_Run") != 1
        || guards.Count(value => value == RunClipName) != 1)
    {
        throw new InvalidOperationException(
            "MonoAnimancer.PlayRun does not contain exactly the Skin25, Byakuya and Bankai guards");
    }
    if (monoAnimancer.Fields.Any(field => field.Name == StateFieldName)
        || monoAnimancer.Methods.Any(method => method.Name == LoopMethodName))
    {
        throw new InvalidOperationException("legacy Bankai Run callback members remain");
    }

    var instructionSet = playRun.Body.Instructions.ToHashSet();
    foreach (var instruction in playRun.Body.Instructions)
    {
        if (instruction.Operand is Instruction target && !instructionSet.Contains(target))
            throw new InvalidOperationException("MonoAnimancer.PlayRun contains an invalid branch target");
        if (instruction.Operand is Instruction[] targets && targets.Any(target => !instructionSet.Contains(target)))
            throw new InvalidOperationException("MonoAnimancer.PlayRun contains an invalid switch target");
    }
}

sealed class NoResolveAssemblyResolver : IAssemblyResolver
{
    public AssemblyDefinition Resolve(AssemblyNameReference name) =>
        throw new AssemblyResolutionException(name);

    public AssemblyDefinition Resolve(AssemblyNameReference name, ReaderParameters parameters) =>
        throw new AssemblyResolutionException(name);

    public void Dispose() { }
}
