using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 2)
    throw new ArgumentException("usage: ClientByakuyaTeleportPatcher <input Unity.ModelView.dll> <output Unity.ModelView.dll>");

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
var instructions = playRun.Body.Instructions;

if (!instructions.Any(instruction => instruction.OpCode == OpCodes.Ldstr
        && (string?)instruction.Operand == "SkinByakuya_Run"))
{
    var skin25 = instructions.Single(instruction => instruction.OpCode == OpCodes.Ldstr
        && (string?)instruction.Operand == "Skin25_Run");
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
        throw new InvalidOperationException("MonoAnimancer.PlayRun Skin25 completion guard was not recognized");
    }

    var insertBefore = skipCallback.Next
        ?? throw new InvalidOperationException("MonoAnimancer.PlayRun guard has no continuation");
    var il = playRun.Body.GetILProcessor();
    il.InsertBefore(insertBefore, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(insertBefore, Instruction.Create(OpCodes.Ldfld, (FieldReference)loadClip.Operand));
    il.InsertBefore(insertBefore, Instruction.Create(OpCodes.Callvirt, (MethodReference)getName.Operand));
    il.InsertBefore(insertBefore, Instruction.Create(OpCodes.Ldstr, "SkinByakuya_Run"));
    il.InsertBefore(insertBefore, Instruction.Create(OpCodes.Call, (MethodReference)equality.Operand));
    il.InsertBefore(insertBefore, Instruction.Create(OpCodes.Brtrue, returnState));
}

Verify(playRun);
Directory.CreateDirectory(Path.GetDirectoryName(output)!);
module.Write(output, new WriterParameters { WriteSymbols = false });

using var check = ModuleDefinition.ReadModule(output, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
    AssemblyResolver = new NoResolveAssemblyResolver(),
});
Verify(check.Types.Single(type => type.FullName == "ET.MonoAnimancer")
    .Methods.Single(method => method.Name == "PlayRun"));
Console.WriteLine("patched MonoAnimancer.PlayRun for SkinByakuya flash-step persistence");

static void Verify(MethodDefinition playRun)
{
    var guards = playRun.Body.Instructions
        .Where(instruction => instruction.OpCode == OpCodes.Ldstr)
        .Select(instruction => instruction.Operand as string)
        .Where(value => value is "Skin25_Run" or "SkinByakuya_Run")
        .ToList();
    if (guards.Count(value => value == "Skin25_Run") != 1
        || guards.Count(value => value == "SkinByakuya_Run") != 1)
    {
        throw new InvalidOperationException("MonoAnimancer.PlayRun does not contain exactly both persistent Run guards");
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
