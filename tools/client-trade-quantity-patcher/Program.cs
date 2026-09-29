using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 2)
    throw new ArgumentException("usage: ClientTradeQuantityPatcher <input HotfixView.dll> <output HotfixView.dll>");

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
var resolver = new NoResolveAssemblyResolver();
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    ReadSymbols = false,
    InMemory = true,
    AssemblyResolver = resolver,
});

var result = PatchTradeQuantity(module);
resolver.Populate(module);
Directory.CreateDirectory(Path.GetDirectoryName(output)!);
module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine(result);

static string PatchTradeQuantity(ModuleDefinition module)
{
    const string warehousePrompt = "请输入您要存储的数量：";
    const string tradePrompt = "请输入您要交易的数量：";

    var storeUI = FindType(module, "ET.StoreUI");
    var tradeMode = storeUI.Fields.Single(field => field.Name == "CodexTradeMode"
        && field.IsStatic && field.FieldType.MetadataType == MetadataType.Boolean);
    var state = storeUI.NestedTypes.Single(type => type.Name == "<<AwakeAsync>b__7_1>d");
    var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
    var instructions = moveNext.Body.Instructions;
    var multiRead = instructions.Single(instruction => instruction.OpCode == OpCodes.Ldsfld
        && instruction.Operand is FieldReference field
        && field.DeclaringType.FullName == "ET.BagUI"
        && field.Name == "isMulti");
    var multiBranch = multiRead.Next ?? throw new InvalidOperationException("BagUI.isMulti branch is missing");
    if ((multiBranch.OpCode != OpCodes.Brtrue && multiBranch.OpCode != OpCodes.Brtrue_S)
        || multiBranch.Operand is not Instruction quantityTarget)
        throw new InvalidOperationException("BagUI.isMulti no longer targets the quantity prompt");

    var warehouseText = instructions.Single(instruction => instruction.OpCode == OpCodes.Ldstr
        && Equals(instruction.Operand, warehousePrompt));
    if (instructions.IndexOf(quantityTarget) >= instructions.IndexOf(warehouseText))
        throw new InvalidOperationException("quantity prompt target is not before its text");

    var existingTradeText = instructions.SingleOrDefault(instruction => instruction.OpCode == OpCodes.Ldstr
        && Equals(instruction.Operand, tradePrompt));
    if (existingTradeText != null)
    {
        VerifyPatchedTradePath(instructions, tradeMode, quantityTarget, warehouseText, existingTradeText);
        return "ET.StoreUI: trade quantity prompt already installed; warehouse isMulti branch preserved";
    }

    var il = moveNext.Body.GetILProcessor();
    il.InsertBefore(multiRead, Instruction.Create(OpCodes.Ldsfld, tradeMode));
    il.InsertBefore(multiRead, Instruction.Create(OpCodes.Brtrue, quantityTarget));

    var afterPromptText = warehouseText.Next
        ?? throw new InvalidOperationException("warehouse quantity prompt has no following instruction");
    var tradeText = Instruction.Create(OpCodes.Ldstr, tradePrompt);
    il.InsertBefore(warehouseText, Instruction.Create(OpCodes.Ldsfld, tradeMode));
    il.InsertBefore(warehouseText, Instruction.Create(OpCodes.Brfalse, warehouseText));
    il.InsertBefore(warehouseText, tradeText);
    il.InsertBefore(warehouseText, Instruction.Create(OpCodes.Br, afterPromptText));

    VerifyPatchedTradePath(instructions, tradeMode, quantityTarget, warehouseText, tradeText);
    return "ET.StoreUI: every trade drag opens a quantity prompt; warehouse isMulti branch preserved";
}

static void VerifyPatchedTradePath(Mono.Collections.Generic.Collection<Instruction> instructions,
    FieldDefinition tradeMode, Instruction quantityTarget, Instruction warehouseText, Instruction tradeText)
{
    var tradeQuantityBranch = instructions.Any(instruction => instruction.OpCode == OpCodes.Ldsfld
        && instruction.Operand is FieldReference field
        && field.FullName == tradeMode.FullName
        && instruction.Next is { Operand: Instruction target }
        && (instruction.Next.OpCode == OpCodes.Brtrue || instruction.Next.OpCode == OpCodes.Brtrue_S)
        && target == quantityTarget);
    if (!tradeQuantityBranch)
        throw new InvalidOperationException("trade mode does not branch to the quantity prompt");

    var promptSelector = instructions.Any(instruction => instruction.OpCode == OpCodes.Ldsfld
        && instruction.Operand is FieldReference field
        && field.FullName == tradeMode.FullName
        && instruction.Next is { Operand: Instruction target }
        && (instruction.Next.OpCode == OpCodes.Brfalse || instruction.Next.OpCode == OpCodes.Brfalse_S)
        && target == warehouseText);
    if (!promptSelector || tradeText.Next?.Operand != warehouseText.Next)
        throw new InvalidOperationException("trade and warehouse prompt texts are not isolated");

    var multiRead = instructions.Single(instruction => instruction.OpCode == OpCodes.Ldsfld
        && instruction.Operand is FieldReference field
        && field.DeclaringType.FullName == "ET.BagUI"
        && field.Name == "isMulti");
    if (multiRead.Next?.Operand != quantityTarget
        || (multiRead.Next.OpCode != OpCodes.Brtrue && multiRead.Next.OpCode != OpCodes.Brtrue_S))
        throw new InvalidOperationException("warehouse isMulti quantity branch changed");
}

static TypeDefinition FindType(ModuleDefinition module, string fullName)
    => AllTypes(module).Single(type => type.FullName == fullName);

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

sealed class NoResolveAssemblyResolver : IAssemblyResolver
{
    private readonly Dictionary<string, AssemblyDefinition> assemblies = new(StringComparer.OrdinalIgnoreCase);

    public AssemblyDefinition Resolve(AssemblyNameReference name)
        => Resolve(name, new ReaderParameters { AssemblyResolver = this });

    public AssemblyDefinition Resolve(AssemblyNameReference name, ReaderParameters parameters)
    {
        if (assemblies.TryGetValue(name.FullName, out var existing))
            return existing;
        var assembly = AssemblyDefinition.CreateAssembly(
            new AssemblyNameDefinition(name.Name, name.Version),
            name.Name,
            ModuleKind.Dll);
        assemblies[name.FullName] = assembly;
        return assembly;
    }

    public void Populate(ModuleDefinition module)
    {
        foreach (var reference in module.AssemblyReferences)
            Resolve(reference);
    }

    public void Dispose()
    {
        foreach (var assembly in assemblies.Values)
            assembly.Dispose();
        assemblies.Clear();
    }
}
