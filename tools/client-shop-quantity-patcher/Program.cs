using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 2)
    throw new ArgumentException("usage: ClientShopQuantityPatcher <input HotfixView.dll> <output HotfixView.dll>");

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
var resolver = new NoResolveAssemblyResolver();
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    ReadSymbols = false,
    InMemory = true,
    AssemblyResolver = resolver,
});

var changes = new List<string>();
PatchBranch(module, "ET.ShopUI", "<<ShowItems>b__1>d", "MoveNext", changes);
PatchBranch(module, "ET.MarketUI", "<>c__DisplayClass3_2", "<AwakeAsync>b__5", changes);
PatchOnlyYuanBaoHint(module, changes);
PatchMarketDirectSend(module, changes);
if (changes.Count != 4)
    throw new InvalidOperationException($"expected four purchase fixes, changed {changes.Count}");

resolver.Populate(module);
Directory.CreateDirectory(Path.GetDirectoryName(output)!);
module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine(string.Join(Environment.NewLine, changes));

static void PatchBranch(ModuleDefinition module, string typeName, string nestedName, string methodName, List<string> changes)
{
    var type = FindType(module, typeName);
    var nested = AllNestedTypes(type).SingleOrDefault(item => item.Name == nestedName)
        ?? throw new InvalidOperationException($"{typeName}: nested type not found; got {string.Join(", ", AllNestedTypes(type).Select(item => item.Name))}");
    var method = nested.Methods.SingleOrDefault(item => item.Name == methodName)
        ?? throw new InvalidOperationException($"{typeName}/{nestedName}: method not found; got {string.Join(", ", nested.Methods.Select(item => item.Name))}");
    var instructions = method.Body.Instructions;
    var il = method.Body.GetILProcessor();
    var ctrlRead = instructions.SingleOrDefault(item =>
        item.OpCode == OpCodes.Callvirt
        && item.Operand is MethodReference methodRef
        && methodRef.Name == "get_ctrl")
        ?? throw new InvalidOperationException($"{typeName}/{nestedName}.{methodName}: get_ctrl call is missing");
    var next = instructions.Skip(instructions.IndexOf(ctrlRead) + 1).FirstOrDefault();
    if (next == null)
        throw new InvalidOperationException($"{typeName}/{nestedName}.{methodName}: get_ctrl branch is missing");
    Instruction target;
    if (next.OpCode == OpCodes.Br || next.OpCode == OpCodes.Br_S)
    {
        if (next.Operand is not Instruction unconditionalTarget)
            throw new InvalidOperationException($"{typeName}: existing quantity branch has no target");
        target = unconditionalTarget;
    }
    else if (next.OpCode == OpCodes.Brtrue || next.OpCode == OpCodes.Brtrue_S)
    {
        if (next.Operand is not Instruction conditionalTarget)
            throw new InvalidOperationException($"{typeName}: ctrl branch has no target");
        target = conditionalTarget;
    }
    else
    {
        throw new InvalidOperationException($"{typeName}/{nestedName}.{methodName}: unexpected instruction after get_ctrl: {next.OpCode}");
    }

    // get_ctrl leaves a bool on the evaluation stack. Consume it before the
    // unconditional jump; otherwise the CLR rejects the method as invalid IL.
    next.OpCode = OpCodes.Pop;
    next.Operand = null;
    var branch = Instruction.Create(OpCodes.Br, target);
    il.InsertAfter(next, branch);
    branch.OpCode = target.Offset - branch.Offset <= sbyte.MaxValue
        ? OpCodes.Br_S
        : OpCodes.Br;
    changes.Add($"{typeName}/{nestedName}.{methodName}: ctrl value consumed and quantity branch is unconditional at IL_{target.Offset:X4}");
}

static void PatchOnlyYuanBaoHint(ModuleDefinition module, List<string> changes)
{
    var type = FindType(module, "ET.MarketUI");
    var nested = AllNestedTypes(type).Single(item => item.Name == "<>c__DisplayClass3_2");
    var method = nested.Methods.Single(item => item.Name == "<AwakeAsync>b__5");
    var instructions = method.Body.Instructions;
    var getter = instructions.Single(item => item.OpCode == OpCodes.Ldfld
        && item.Operand is FieldReference field
        && field.Name == "OnlyYuanBao");
    var branch = instructions.Skip(instructions.IndexOf(getter) + 1).First(item =>
        item.OpCode == OpCodes.Brfalse || item.OpCode == OpCodes.Brfalse_S);
    if (branch.Operand is not Instruction target)
        throw new InvalidOperationException("OnlyYuanBao branch has no target");
    var il = method.Body.GetILProcessor();
    // Keep the original localized warning code in place but make the data
    // flag informational: voucher and yuanbao tabs both reach SendBuyProto.
    branch.OpCode = OpCodes.Pop;
    branch.Operand = null;
    var jump = Instruction.Create(OpCodes.Br, target);
    il.InsertAfter(branch, jump);
    jump.OpCode = target.Offset - jump.Offset <= sbyte.MaxValue ? OpCodes.Br_S : OpCodes.Br;
    changes.Add("ET.MarketUI/<AwakeAsync>b__5: OnlyYuanBao hint no longer blocks voucher purchases");
}

static void PatchMarketDirectSend(ModuleDefinition module, List<string> changes)
{
    var type = FindType(module, "ET.MarketUI");
    var owner = AllNestedTypes(type).Single(item => item.Name == "<>c__DisplayClass3_2");
    var confirm = AllNestedTypes(type).Single(item => item.Name == "<>c__DisplayClass3_4")
        .Methods.Single(item => item.Name == "<AwakeAsync>b__8");
    var method = owner.Methods.Single(item => item.Name == "<AwakeAsync>g__SendBuyProto|7");
    var countStore = method.Body.Instructions.Single(item => item.OpCode == OpCodes.Stfld
        && item.Operand is FieldReference field
        && field.Name == "count");
    var il = method.Body.GetILProcessor();
    foreach (var instruction in method.Body.Instructions.Skip(method.Body.Instructions.IndexOf(countStore) + 1).ToList())
        il.Remove(instruction);
    il.Append(Instruction.Create(OpCodes.Ldloc_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(confirm)));
    il.Append(Instruction.Create(OpCodes.Ret));
    changes.Add("ET.MarketUI: quantity confirmation sends C2M_BuyInMarket immediately");
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
        foreach (var reference in module.GetTypeReferences())
        {
            if (reference.Scope is not AssemblyNameReference assemblyName)
                continue;
            var assembly = Resolve(assemblyName).MainModule;
            if (assembly.GetType(reference.FullName) != null)
                continue;
            var type = new TypeDefinition(reference.Namespace, reference.Name,
                TypeAttributes.Public | TypeAttributes.Class,
                null);
            assembly.Types.Add(type);
        }
    }

    public void Dispose()
    {
        foreach (var assembly in assemblies.Values)
            assembly.Dispose();
        assemblies.Clear();
    }
}
