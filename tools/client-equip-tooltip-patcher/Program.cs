using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 2)
    throw new ArgumentException("usage: ClientEquipTooltipPatcher <input HotfixView.dll> <output HotfixView.dll>");

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
    AssemblyResolver = new NoResolveAssemblyResolver(),
});

var characterUI = module.Types.Single(type => type.FullName == "ET.CharacterUI");
var display = AllNestedTypes(characterUI).Single(type => type.Name == "<>c__DisplayClass19_0");
var callback = display.Methods.Single(method => method.Name == "<AddRollEvent>b__0");
var instructions = callback.Body.Instructions;
var selectIndex = instructions
    .Select((item, index) => (item, index))
    .Where(pair => pair.item.OpCode == OpCodes.Callvirt
        && pair.item.Operand is MethodReference method
        && method.Name == "get_SelectUnitId")
    .Select(pair => pair.index)
    .SingleOrDefault(-1);
var thisField = callback.DeclaringType.Fields.Single(field => field.Name == "<>4__this");
var idField = characterUI.Fields.Single(field => field.Name == "id");
if (selectIndex >= 1)
{
    var localLoad = instructions[selectIndex - 1];
    if (localLoad.OpCode != OpCodes.Ldloc_0)
        throw new InvalidOperationException($"unexpected instruction before SelectUnitId: {localLoad.OpCode}");
    var il = callback.Body.GetILProcessor();
    localLoad.OpCode = OpCodes.Ldarg_0;
    localLoad.Operand = null;
    il.InsertAfter(localLoad, Instruction.Create(OpCodes.Ldfld, module.ImportReference(thisField)));
    il.InsertAfter(localLoad.Next, Instruction.Create(OpCodes.Ldfld, module.ImportReference(idField)));
    il.Remove(instructions[selectIndex + 2]);
}
else if (!instructions.Any(instruction => instruction.OpCode == OpCodes.Ldfld
    && instruction.Operand is FieldReference field && field.Name == idField.Name))
{
    throw new InvalidOperationException("CharacterUI tooltip callback does not use CharacterUI.id");
}

// Remote character windows do not necessarily have a StarSoulBag entry for
// every equipment type. The original indexer throws before the rest of the
// equipment tooltip can be rendered; use TryGetValue and skip only that block.
var tooltip = module.Types.Single(type => type.FullName == "ET.TabHelper").Methods
    .Single(method => method.Name == "OpenUI" && method.HasBody && method.Body.Variables.Count > 18
        && method.Body.Instructions.Any(instruction => instruction.OpCode == OpCodes.Callvirt
            && instruction.Operand is MethodReference reference && reference.Name == "get_Item"
            && instruction.Previous?.OpCode == OpCodes.Conv_U1));
var getItem = tooltip.Body.Instructions.First(instruction => instruction.OpCode == OpCodes.Callvirt
    && instruction.Operand is MethodReference reference && reference.Name == "get_Item"
    && instruction.Previous?.OpCode == OpCodes.Conv_U1);
var getItemReference = (MethodReference)getItem.Operand;
var valueLocal = tooltip.Body.Variables[18];
var tryGetValue = new MethodReference("TryGetValue", module.TypeSystem.Boolean,
    module.ImportReference(getItemReference.DeclaringType))
{
    HasThis = true,
};
tryGetValue.Parameters.Add(new ParameterDefinition(module.ImportReference(getItemReference.Parameters[0].ParameterType)));
tryGetValue.Parameters.Add(new ParameterDefinition(new ByReferenceType(module.ImportReference(getItemReference.ReturnType))));
var oldStore = getItem.Next;
var oldLoad = oldStore?.Next;
var oldBranch = oldLoad?.Next;
if (oldStore?.OpCode != OpCodes.Stloc_S || oldLoad?.OpCode != OpCodes.Ldloc_S
    || oldBranch?.OpCode != OpCodes.Brfalse_S || oldBranch.Operand is not Instruction skipStarSoul)
    throw new InvalidOperationException("StarSoulBag indexer block was not structurally recognized");
var tooltipIL = tooltip.Body.GetILProcessor();
tooltipIL.InsertBefore(getItem, Instruction.Create(OpCodes.Ldloca_S, valueLocal));
getItem.OpCode = OpCodes.Callvirt;
getItem.Operand = module.ImportReference(tryGetValue);
tooltipIL.Remove(oldStore);
tooltipIL.Remove(oldLoad);
tooltipIL.InsertAfter(oldBranch, Instruction.Create(OpCodes.Ldloc_S, valueLocal));
tooltipIL.InsertAfter(oldBranch.Next, Instruction.Create(OpCodes.Brfalse_S, skipStarSoul));

Directory.CreateDirectory(Path.GetDirectoryName(output)!);
module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine("verified CharacterUI tooltip uses the queried character window id and tolerates missing StarSoul entries");

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
            new AssemblyNameDefinition(name.Name, name.Version), name.Name, ModuleKind.Dll);
        assemblies[name.FullName] = assembly;
        return assembly;
    }

    public void Dispose()
    {
        foreach (var assembly in assemblies.Values)
            assembly.Dispose();
        assemblies.Clear();
    }
}
