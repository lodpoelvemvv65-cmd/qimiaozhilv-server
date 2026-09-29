using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 2)
    throw new ArgumentException("usage: ClientSkinRefreshPatcher <input HotfixView.dll> <output HotfixView.dll>");

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
    AssemblyResolver = new NoResolveAssemblyResolver(),
});

var characterUI = module.Types.Single(type => type.FullName == "ET.CharacterUI");
var fui = module.Types.Single(type => type.FullName == "ET.FUI_CharacterUI");
var idField = characterUI.Fields.Single(field => field.Name == "id");
var skinTranField = characterUI.Fields.Single(field => field.Name == "skinTran");
var itemType = FindFieldReference(module, "ET.ClientItemData", "ItemType");
var itemId = FindFieldReference(module, "ET.ClientItemData", "ItemId");
var itemData = module.ImportReference(itemType.DeclaringType);
var itemComponentInstance = FindMethod(module, "ET.ClientItemDataComponent", "get_Instance");
var wornGetter = FindMethod(module, "ET.ClientItemDataComponent", "get_WornEquipDic");
var tryGetValue = FindTryGetValue(characterUI);
var getParent = FindGetParentCall(module, characterUI);
var characterComponent = FindType(module, "ET", "ClientUnitCharacterComponent", "Unity.Model");
var character = FindType(module, "ET", "ClientUnitCharacter", "Unity.Model");
var getInstance = FindMethod(module, "ET.ClientUnitCharacterComponent", "get_Instance");
var getById = FindMethod(module, "ET.ClientUnitCharacterComponent", "Get");
var getJobId = FindMethod(module, "ET.ClientUnitCharacter", "get_JobId");
var showSkin = characterUI.Methods.Single(method => method.Name == "ShowSkin" && method.Parameters.Count == 1);
var coroutine = FindMethod(module, "ET.ETVoid", "Coroutine");
var destroyPrefab = module.Types.SelectMany(AllTypes).Single(type => type.FullName == "ET.ResourceViewHelper")
    .Methods.Single(method => method.Name == "DestoryPrefabAsync" && method.Parameters.Count == 1
        && method.Parameters[0].ParameterType.FullName == skinTranField.FieldType.FullName);
var displayedSkinId = AddField(characterUI, "characterDisplayedSkinId", module.TypeSystem.Int32);
PatchShowSkinTracking(showSkin, displayedSkinId);

var refresh = AddRefreshMethod(module, characterUI, characterComponent, character, itemData, idField, getInstance,
    getById, getJobId, itemComponentInstance, wornGetter, tryGetValue, itemType, itemId, showSkin, coroutine,
    skinTranField, destroyPrefab, displayedSkinId);
PatchEvent(module, characterUI, fui, getParent, refresh);
var (getFuiInstance, getFui) = FindCharacterUIGetCalls(module);
var refreshFromEvent = AddRefreshFromEventMethod(module, characterUI, fui, getParent, getFuiInstance, getFui, refresh);
PatchCharacterUpdateRun(module, refreshFromEvent);

module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine("installed CharacterUI immediate skin refresh");

static MethodDefinition AddRefreshMethod(ModuleDefinition module, TypeDefinition characterUI, TypeReference component,
    TypeReference character, TypeReference itemData, FieldDefinition idField, MethodReference getInstance,
    MethodReference getById, MethodReference getJobId, MethodReference itemComponentInstance,
    MethodReference wornGetter, MethodReference tryGetValue, FieldReference itemType, FieldReference itemId,
    MethodDefinition showSkin, MethodReference coroutine, FieldDefinition skinTranField,
    MethodDefinition destroyPrefab, FieldDefinition displayedSkinId)
{
    var method = characterUI.Methods.FirstOrDefault(candidate => candidate.Name == "RefreshCharacterSkin")
        ?? new MethodDefinition("RefreshCharacterSkin", MethodAttributes.Public, module.TypeSystem.Void);
    if (!characterUI.Methods.Contains(method))
        characterUI.Methods.Add(method);
    else
        method.Body = new MethodBody(method);
    method.Body.InitLocals = true;
    var unit = new VariableDefinition(character);
    var data = new VariableDefinition(itemData);
    var desiredSkinId = new VariableDefinition(module.TypeSystem.Int32);
    var task = new VariableDefinition(showSkin.ReturnType);
    method.Body.Variables.Add(unit);
    method.Body.Variables.Add(data);
    method.Body.Variables.Add(desiredSkinId);
    method.Body.Variables.Add(task);
    var il = method.Body.GetILProcessor();
    var desiredReady = Instruction.Create(OpCodes.Nop);
    var noOldSkin = Instruction.Create(OpCodes.Nop);
    var done = Instruction.Create(OpCodes.Ret);

    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(getInstance)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, idField));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getById)));
    il.Append(Instruction.Create(OpCodes.Stloc, unit));
    il.Append(Instruction.Create(OpCodes.Ldloc, unit));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldloc, unit));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getJobId)));
    il.Append(Instruction.Create(OpCodes.Stloc, desiredSkinId));

    // WornEquipDic is updated before this hook runs. Slot 2 is the skin slot;
    // an explicit ItemType=0 entry means the skin was taken off.
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(itemComponentInstance)));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(wornGetter)));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Ldloca, data));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(tryGetValue)));
    il.Append(Instruction.Create(OpCodes.Brfalse, desiredReady));
    il.Append(Instruction.Create(OpCodes.Ldloc, data));
    il.Append(Instruction.Create(OpCodes.Brfalse, desiredReady));
    il.Append(Instruction.Create(OpCodes.Ldloc, data));
    il.Append(Instruction.Create(OpCodes.Ldfld, itemType));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Bne_Un, desiredReady));
    il.Append(Instruction.Create(OpCodes.Ldloc, data));
    il.Append(Instruction.Create(OpCodes.Ldfld, itemId));
    il.Append(Instruction.Create(OpCodes.Brfalse, desiredReady));
    il.Append(Instruction.Create(OpCodes.Ldloc, data));
    il.Append(Instruction.Create(OpCodes.Ldfld, itemId));
    il.Append(Instruction.Create(OpCodes.Stloc, desiredSkinId));
    il.Append(desiredReady);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, displayedSkinId));
    il.Append(Instruction.Create(OpCodes.Ldloc, desiredSkinId));
    il.Append(Instruction.Create(OpCodes.Beq, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, desiredSkinId));
    il.Append(Instruction.Create(OpCodes.Stfld, displayedSkinId));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, skinTranField));
    il.Append(Instruction.Create(OpCodes.Brfalse, noOldSkin));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, skinTranField));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(destroyPrefab)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldnull));
    il.Append(Instruction.Create(OpCodes.Stfld, skinTranField));
    il.Append(noOldSkin);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, desiredSkinId));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(showSkin)));
    il.Append(Instruction.Create(OpCodes.Stloc, task));
    il.Append(Instruction.Create(OpCodes.Ldloca, task));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(coroutine)));
    il.Append(done);
    il.Append(Instruction.Create(OpCodes.Ret));
    return method;
}

static void PatchEvent(ModuleDefinition module, TypeDefinition characterUI, TypeDefinition fui,
    MethodReference getParent, MethodDefinition refresh)
{
    var state = module.Types.SelectMany(AllNestedTypes)
        .Single(type => type.FullName == "ET.UpdateWornEquipUIEvent/<Run>d__0");
    var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
    if (moveNext.Body.Instructions.Any(instruction => instruction.Operand is MethodReference reference
        && reference.Name == refresh.Name))
        return;
    var current = moveNext.Body.Variables.First(variable => variable.VariableType.FullName == fui.FullName);
    var owner = new VariableDefinition(characterUI);
    moveNext.Body.Variables.Add(owner);
    var skip = Instruction.Create(OpCodes.Nop);
    var result = moveNext.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference reference && reference.Name == "SetResult");
    var il = moveNext.Body.GetILProcessor();
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, current));
    il.InsertBefore(result, Instruction.Create(OpCodes.Brfalse, skip));
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, current));
    il.InsertBefore(result, Instruction.Create(OpCodes.Call, module.ImportReference(getParent)));
    il.InsertBefore(result, Instruction.Create(OpCodes.Stloc, owner));
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, owner));
    il.InsertBefore(result, Instruction.Create(OpCodes.Brfalse, skip));
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, owner));
    il.InsertBefore(result, Instruction.Create(OpCodes.Callvirt, module.ImportReference(refresh)));
    il.InsertBefore(result, skip);
}

static MethodDefinition AddRefreshFromEventMethod(ModuleDefinition module, TypeDefinition characterUI,
    TypeDefinition fui, MethodReference getParent, MethodReference getFuiInstance, MethodReference getFui,
    MethodDefinition refresh)
{
    var method = characterUI.Methods.FirstOrDefault(candidate => candidate.Name == "RefreshCharacterSkinFromEvent")
        ?? new MethodDefinition("RefreshCharacterSkinFromEvent",
            MethodAttributes.Public | MethodAttributes.Static, module.TypeSystem.Void);
    if (!characterUI.Methods.Contains(method))
        characterUI.Methods.Add(method);
    else
        method.Body = new MethodBody(method);
    method.Body.InitLocals = true;
    var current = new VariableDefinition(fui);
    var owner = new VariableDefinition(characterUI);
    method.Body.Variables.Add(current);
    method.Body.Variables.Add(owner);
    var done = Instruction.Create(OpCodes.Ret);
    var il = method.Body.GetILProcessor();

    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(getFuiInstance)));
    il.Append(Instruction.Create(OpCodes.Ldstr, "ui://Character/CharacterUI"));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getFui)));
    il.Append(Instruction.Create(OpCodes.Isinst, module.ImportReference(fui)));
    il.Append(Instruction.Create(OpCodes.Stloc, current));
    il.Append(Instruction.Create(OpCodes.Ldloc, current));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldloc, current));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(getParent)));
    il.Append(Instruction.Create(OpCodes.Stloc, owner));
    il.Append(Instruction.Create(OpCodes.Ldloc, owner));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldloc, owner));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(refresh)));
    il.Append(done);
    il.Append(Instruction.Create(OpCodes.Ret));
    return method;
}

static void PatchCharacterUpdateRun(ModuleDefinition module, MethodDefinition refreshFromEvent)
{
    var eventType = module.Types.Single(type => type.FullName == "ET.UpdateCharacterUIEvent");
    var run = eventType.Methods.Single(method => method.Name == "Run" && method.Parameters.Count == 1);
    foreach (var instruction in run.Body.Instructions.Where(instruction =>
                 instruction.Operand is MethodReference reference && reference.Name == refreshFromEvent.Name).ToList())
        run.Body.GetILProcessor().Remove(instruction);

    var state = eventType.NestedTypes.Single(type => type.Name == "<Run>d__0");
    var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
    if (moveNext.Body.Instructions.Any(instruction => instruction.Operand is MethodReference reference
        && reference.Name == refreshFromEvent.Name))
        return;
    var result = moveNext.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference reference && reference.Name == "SetResult");
    moveNext.Body.GetILProcessor().InsertBefore(result, Instruction.Create(OpCodes.Call,
        module.ImportReference(refreshFromEvent)));
}

static void PatchShowSkinTracking(MethodDefinition showSkin, FieldDefinition displayedSkinId)
{
    if (showSkin.Body.Instructions.Any(instruction => instruction.Operand is FieldReference field
        && field.Name == displayedSkinId.Name))
        return;
    var first = showSkin.Body.Instructions[0];
    var il = showSkin.Body.GetILProcessor();
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_1));
    il.InsertBefore(first, Instruction.Create(OpCodes.Stfld, displayedSkinId));
}

static FieldDefinition AddField(TypeDefinition type, string name, TypeReference fieldType)
{
    var existing = type.Fields.FirstOrDefault(field => field.Name == name);
    if (existing != null)
        return existing;
    var field = new FieldDefinition(name, FieldAttributes.Private, fieldType);
    type.Fields.Add(field);
    return field;
}

static (MethodReference getInstance, MethodReference get) FindCharacterUIGetCalls(ModuleDefinition module)
{
    foreach (var method in module.Types.SelectMany(AllMethods))
    {
        var instructions = method.Body?.Instructions;
        if (instructions == null)
            continue;
        for (var index = 0; index + 2 < instructions.Count; index++)
        {
            if (instructions[index].Operand is not MethodReference instance
                || instance.Name != "get_Instance")
                continue;
            for (var next = index + 1; next < Math.Min(index + 5, instructions.Count); next++)
            {
                if (instructions[next].Operand is MethodReference getter && getter.Name == "Get"
                    && getter.DeclaringType.FullName.Contains("FUIComponent", StringComparison.Ordinal))
                    return (instance, getter);
            }
        }
    }
    throw new InvalidOperationException("FUIComponent instance/Get references not found");
}

static MethodReference FindGetParentCall(ModuleDefinition module, TypeDefinition characterUI)
{
    foreach (var method in module.Types.SelectMany(AllMethods))
    foreach (var instruction in method.Body?.Instructions ?? Enumerable.Empty<Instruction>())
    {
        if (instruction.Operand is GenericInstanceMethod generic && generic.Name == "GetParent")
        {
            var copy = new GenericInstanceMethod(generic.ElementMethod);
            copy.GenericArguments.Add(characterUI);
            return module.ImportReference(copy);
        }
    }
    throw new InvalidOperationException("Entity.GetParent<T> reference not found");
}

static MethodReference FindMethod(ModuleDefinition module, string declaringType, string name)
{
    foreach (var method in module.Types.SelectMany(AllMethods))
    foreach (var instruction in method.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        if (instruction.Operand is MethodReference reference && reference.Name == name
            && reference.DeclaringType.FullName == declaringType)
            return reference;
    throw new InvalidOperationException($"method reference {declaringType}::{name} not found");
}

static FieldReference FindFieldReference(ModuleDefinition module, string declaringType, string name)
{
    foreach (var method in module.Types.SelectMany(AllMethods))
    foreach (var instruction in method.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        if (instruction.Operand is FieldReference reference && reference.Name == name
            && reference.DeclaringType.FullName == declaringType)
            return reference;
    throw new InvalidOperationException($"field reference {declaringType}::{name} not found");
}

static MethodReference FindTryGetValue(TypeDefinition characterUI)
{
    foreach (var method in AllMethods(characterUI))
    foreach (var instruction in method.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        if (instruction.Operand is MethodReference reference && reference.Name == "TryGetValue")
            return reference;
    throw new InvalidOperationException("WornEquipDic.TryGetValue reference not found");
}

static TypeReference FindType(ModuleDefinition module, string ns, string name, string assembly)
{
    var scope = module.AssemblyReferences.FirstOrDefault(reference => reference.Name == assembly);
    if (scope == null)
    {
        scope = new AssemblyNameReference(assembly, new Version(0, 0, 0, 0));
        module.AssemblyReferences.Add(scope);
    }
    return new TypeReference(ns, name, module, scope, false);
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

static IEnumerable<TypeDefinition> AllTypes(TypeDefinition type)
{
    yield return type;
    foreach (var nested in type.NestedTypes)
        foreach (var child in AllTypes(nested))
            yield return child;
}

static IEnumerable<MethodDefinition> AllMethods(TypeDefinition type)
{
    foreach (var method in type.Methods)
        yield return method;
    foreach (var nested in type.NestedTypes)
        foreach (var method in AllMethods(nested))
            yield return method;
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
