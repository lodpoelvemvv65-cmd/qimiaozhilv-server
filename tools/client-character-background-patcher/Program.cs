using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 2)
    throw new ArgumentException("usage: ClientCharacterBackgroundPatcher <input HotfixView.dll> <output HotfixView.dll>");

var input = Path.GetFullPath(args[0]);
var output = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
    AssemblyResolver = new NoResolveAssemblyResolver(),
});

var characterUI = module.Types.Single(type => type.FullName == "ET.CharacterUI");
var fuiCharacterUI = module.Types.Single(type => type.FullName == "ET.FUI_CharacterUI");
var itemType = FindFieldReference(module, "ET.ClientItemData", "ItemType");
var itemId = FindFieldReference(module, "ET.ClientItemData", "ItemId");
var clientItemData = module.ImportReference(itemType.DeclaringType);
var wornGetter = FindMethod(module, "ET.ClientItemDataComponent", "get_WornEquipDic");
var instanceGetter = FindMethod(module, "ET.ClientItemDataComponent", "get_Instance");
var tryGetValue = FindTryGetValue(characterUI);
var uiField = characterUI.Fields.Single(field => field.Name == "ui");
var bgField = fuiCharacterUI.Fields.Single(field => field.Name == "m_bg");
var gLoader = module.ImportReference(bgField.FieldType);
var setTexture = FindMethod(module, "FairyGUI.GLoader", "set_texture");
var nTextureType = FindType(module, "FairyGUI", "NTexture", "Unity.ThirdParty");
var unityTexture = FindType(module, "UnityEngine", "Texture", "UnityEngine.CoreModule");
var texture2D = FindType(module, "UnityEngine", "Texture2D", "UnityEngine.CoreModule");
var textureFormat = FindType(module, "UnityEngine", "TextureFormat", "UnityEngine.CoreModule", true);
var unityObject = FindType(module, "UnityEngine", "Object", "UnityEngine.CoreModule");
var application = FindType(module, "UnityEngine", "Application", "UnityEngine.CoreModule");
var imageConversion = FindType(module, "UnityEngine", "ImageConversion", "UnityEngine.ImageConversionModule");
var pathType = FindType(module, "System.IO", "Path", "mscorlib");
var fileType = FindType(module, "System.IO", "File", "mscorlib");
var byteArray = new ArrayType(module.TypeSystem.Byte);

var backgroundTexture = AddField(characterUI, "characterBackgroundTexture", texture2D);
var backgroundId = AddField(characterUI, "characterBackgroundId", module.TypeSystem.Int32);
var backgroundLoader = AddField(characterUI, "characterBackgroundLoader", gLoader);

var clear = AddClearBackground(module, characterUI, unityObject, backgroundTexture, backgroundId, backgroundLoader);
var refresh = AddRefreshBackground(module, characterUI, clientItemData, instanceGetter, wornGetter, tryGetValue,
    itemType, itemId, fuiCharacterUI, uiField, bgField, gLoader, setTexture, nTextureType, unityTexture, texture2D,
    textureFormat, application, imageConversion, pathType, fileType, byteArray, backgroundTexture, backgroundId,
    backgroundLoader, clear);

PatchDestroy(characterUI, clear);
PatchWornEvent(module, characterUI, refresh);
PatchCharacterRefresh(characterUI, refresh);
PatchCharacterAwakeAsync(characterUI, refresh);
VerifyRefreshBackground(refresh);

module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine("installed CharacterUI foreground background layer and worn-equipment refresh");

static void VerifyRefreshBackground(MethodDefinition refresh)
{
    var instructions = refresh.Body.Instructions;
    var textureConstructor = instructions
        .FirstOrDefault(instruction => instruction.OpCode == OpCodes.Newobj
            && instruction.Operand is MethodReference reference
            && reference.Name == ".ctor"
            && reference.DeclaringType.FullName == "UnityEngine.Texture2D"
            && reference.Parameters.Count == 4);
    if (textureConstructor?.Operand is not MethodReference textureConstructorReference
        || !textureConstructorReference.Parameters[2].ParameterType.IsValueType)
        throw new InvalidOperationException("Texture2D constructor requires TextureFormat to be a value type");
    var constructorIndex = instructions.IndexOf(textureConstructor);
    if (constructorIndex < 4 || instructions[constructorIndex - 1].OpCode != OpCodes.Ldc_I4_0)
        throw new InvalidOperationException("Texture2D constructor requires mipChain=false as its fourth argument");

    for (var index = 1; index < instructions.Count; index++)
    {
        if (instructions[index].Operand is not MethodReference reference
            || reference.DeclaringType.FullName != "System.Int32"
            || reference.Name != "ToString"
            || reference.Parameters.Count != 0)
            continue;
        if (instructions[index - 1].OpCode != OpCodes.Ldloca
            && instructions[index - 1].OpCode != OpCodes.Ldloca_S)
            throw new InvalidOperationException("Int32.ToString requires the address of the background id local");
        VerifyOverlayLayer();
        return;
    }
    throw new InvalidOperationException("background id ToString call not found");

    void VerifyOverlayLayer()
    {
        var addIndex = instructions.ToList().FindIndex(instruction =>
            instruction.Operand is MethodReference reference
            && reference.DeclaringType.FullName == "FairyGUI.GComponent"
            && reference.Name == "AddChildAt");
        if (addIndex < 1 || instructions[addIndex - 1].OpCode != OpCodes.Ldc_I4_2)
            throw new InvalidOperationException("background loader must be inserted at CharacterUI child index 2");
        if (!instructions.Any(instruction => instruction.Operand is FieldReference field
                && field.Name == "characterBackgroundLoader"))
            throw new InvalidOperationException("dedicated character background loader field is not used");
        if (!instructions.Any(instruction => instruction.Operand is MethodReference reference
                && reference.DeclaringType.FullName == "FairyGUI.GLoader"
                && reference.Name == "set_fill"))
            throw new InvalidOperationException("background loader fill mode is not configured");
        if (!instructions.Any(instruction => instruction.Operand is MethodReference reference
                && reference.DeclaringType.FullName == "FairyGUI.GObject"
                && reference.Name == "set_group"))
            throw new InvalidOperationException("background loader must follow the original background group");
        if (instructions.Any(instruction => instruction.Operand is MethodReference reference
                && reference.DeclaringType.FullName == "ET.Log"))
            throw new InvalidOperationException("temporary CharacterBackground diagnostics must not ship");
    }
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

static MethodDefinition AddClearBackground(ModuleDefinition module, TypeDefinition characterUI,
    TypeReference unityObject, FieldDefinition backgroundTexture, FieldDefinition backgroundId,
    FieldDefinition backgroundLoader)
{
    var existing = characterUI.Methods.FirstOrDefault(method => method.Name == "RefreshCharacterBackgroundClear");
    var method = existing ?? new MethodDefinition("RefreshCharacterBackgroundClear", MethodAttributes.Private,
        module.TypeSystem.Void);
    if (existing == null)
        characterUI.Methods.Add(method);
    else
        method.Body = new MethodBody(method);
    var il = method.Body.GetILProcessor();
    var gObject = FindType(module, "FairyGUI", "GObject", "Unity.ThirdParty");
    var dispose = new MethodReference("Dispose", module.TypeSystem.Void, gObject) { HasThis = true };
    var destroy = new MethodReference("Destroy", module.TypeSystem.Void, unityObject) { HasThis = false };
    destroy.Parameters.Add(new ParameterDefinition(unityObject));
    var noLoader = Instruction.Create(OpCodes.Nop);
    var noTexture = Instruction.Create(OpCodes.Nop);

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Brfalse, noLoader));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(dispose)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldnull));
    il.Append(Instruction.Create(OpCodes.Stfld, backgroundLoader));
    il.Append(noLoader);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundTexture));
    il.Append(Instruction.Create(OpCodes.Brfalse, noTexture));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundTexture));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(destroy)));
    il.Append(noTexture);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldnull));
    il.Append(Instruction.Create(OpCodes.Stfld, backgroundTexture));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_M1));
    il.Append(Instruction.Create(OpCodes.Stfld, backgroundId));
    il.Append(Instruction.Create(OpCodes.Ret));
    return method;
}

static MethodDefinition AddRefreshBackground(ModuleDefinition module, TypeDefinition characterUI, TypeReference itemData,
    MethodReference instanceGetter, MethodReference wornGetter, MethodReference tryGetValue, FieldReference itemType,
    FieldReference itemId, TypeDefinition fui, FieldDefinition uiField, FieldDefinition bgField,
    TypeReference gLoader, MethodReference setTexture, TypeReference nTexture, TypeReference unityTexture,
    TypeReference texture2D, TypeReference textureFormat, TypeReference application, TypeReference imageConversion,
    TypeReference pathType, TypeReference fileType, ArrayType byteArray, FieldDefinition backgroundTexture,
    FieldDefinition backgroundId, FieldDefinition backgroundLoader, MethodDefinition clear)
{
    var existing = characterUI.Methods.FirstOrDefault(method => method.Name == "RefreshCharacterBackground");
    var method = existing ?? new MethodDefinition("RefreshCharacterBackground", MethodAttributes.Public,
        module.TypeSystem.Void)
    {
        HasThis = true,
    };
    if (existing == null)
        characterUI.Methods.Add(method);
    else
        method.Body = new MethodBody(method);
    method.Body.InitLocals = true;
    var data = new VariableDefinition(itemData);
    var id = new VariableDefinition(module.TypeSystem.Int32);
    var path = new VariableDefinition(module.TypeSystem.String);
    var bytes = new VariableDefinition(byteArray);
    var texture = new VariableDefinition(texture2D);
    method.Body.Variables.Add(data);
    method.Body.Variables.Add(id);
    method.Body.Variables.Add(path);
    method.Body.Variables.Add(bytes);
    method.Body.Variables.Add(texture);
    var il = method.Body.GetILProcessor();
    var clearLabel = Instruction.Create(OpCodes.Nop);
    var noSlot = Instruction.Create(OpCodes.Nop);
    var invalidData = Instruction.Create(OpCodes.Nop);
    var invalidType = Instruction.Create(OpCodes.Nop);
    var invalidId = Instruction.Create(OpCodes.Nop);
    var missingFile = Instruction.Create(OpCodes.Nop);
    var loadFailed = Instruction.Create(OpCodes.Nop);
    var done = Instruction.Create(OpCodes.Ret);
    var replaceExisting = Instruction.Create(OpCodes.Nop);
    var loadNew = Instruction.Create(OpCodes.Nop);
    var imageLoaded = Instruction.Create(OpCodes.Nop);
    var intToString = new MethodReference("ToString", module.TypeSystem.String, module.TypeSystem.Int32)
    {
        HasThis = true,
    };
    var concat = new MethodReference("Concat", module.TypeSystem.String, module.TypeSystem.String)
    {
        HasThis = false,
    };
    concat.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    concat.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));

    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(instanceGetter)));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(wornGetter)));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Ldloca, data));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(tryGetValue)));
    il.Append(Instruction.Create(OpCodes.Brfalse, noSlot));
    il.Append(Instruction.Create(OpCodes.Ldloc, data));
    il.Append(Instruction.Create(OpCodes.Brfalse, invalidData));
    il.Append(Instruction.Create(OpCodes.Ldloc, data));
    il.Append(Instruction.Create(OpCodes.Ldfld, itemType));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Bne_Un, invalidType));
    il.Append(Instruction.Create(OpCodes.Ldloc, data));
    il.Append(Instruction.Create(OpCodes.Ldfld, itemId));
    il.Append(Instruction.Create(OpCodes.Stloc, id));
    il.Append(Instruction.Create(OpCodes.Ldloc, id));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 120890));
    il.Append(Instruction.Create(OpCodes.Blt, invalidId));
    il.Append(Instruction.Create(OpCodes.Ldloc, id));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 120909));
    il.Append(Instruction.Create(OpCodes.Bgt, invalidId));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundTexture));
    il.Append(Instruction.Create(OpCodes.Brfalse, loadNew));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundId));
    il.Append(Instruction.Create(OpCodes.Ldloc, id));
    il.Append(Instruction.Create(OpCodes.Bne_Un, replaceExisting));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Brtrue, done));
    il.Append(Instruction.Create(OpCodes.Br, replaceExisting));
    il.Append(replaceExisting);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Call, clear));
    il.Append(loadNew);

    var streamingPath = new MethodReference("get_streamingAssetsPath", module.TypeSystem.String, application)
    {
        HasThis = false,
    };
    var combine = new MethodReference("Combine", module.TypeSystem.String, pathType) { HasThis = false };
    combine.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    combine.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    var exists = new MethodReference("Exists", module.TypeSystem.Boolean, fileType) { HasThis = false };
    exists.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    var readAllBytes = new MethodReference("ReadAllBytes", byteArray, fileType) { HasThis = false };
    readAllBytes.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    var textureCtor = new MethodReference(".ctor", module.TypeSystem.Void, texture2D) { HasThis = true };
    textureCtor.Parameters.Add(new ParameterDefinition(module.TypeSystem.Int32));
    textureCtor.Parameters.Add(new ParameterDefinition(module.TypeSystem.Int32));
    textureCtor.Parameters.Add(new ParameterDefinition(textureFormat));
    textureCtor.Parameters.Add(new ParameterDefinition(module.TypeSystem.Boolean));
    var loadImage = new MethodReference("LoadImage", module.TypeSystem.Boolean, imageConversion) { HasThis = false };
    loadImage.Parameters.Add(new ParameterDefinition(texture2D));
    loadImage.Parameters.Add(new ParameterDefinition(byteArray));
    loadImage.Parameters.Add(new ParameterDefinition(module.TypeSystem.Boolean));
    var nTextureCtor = new MethodReference(".ctor", module.TypeSystem.Void, nTexture) { HasThis = true };
    nTextureCtor.Parameters.Add(new ParameterDefinition(unityTexture));
    var gObject = FindType(module, "FairyGUI", "GObject", "Unity.ThirdParty");
    var gComponent = FindType(module, "FairyGUI", "GComponent", "Unity.ThirdParty");
    var gGroup = FindType(module, "FairyGUI", "GGroup", "Unity.ThirdParty");
    var fillType = FindType(module, "FairyGUI", "FillType", "Unity.ThirdParty", true);
    var loaderCtor = new MethodReference(".ctor", module.TypeSystem.Void, gLoader) { HasThis = true };
    var setTouchable = new MethodReference("set_touchable", module.TypeSystem.Void, gObject) { HasThis = true };
    setTouchable.Parameters.Add(new ParameterDefinition(module.TypeSystem.Boolean));
    var setFill = new MethodReference("set_fill", module.TypeSystem.Void, gLoader) { HasThis = true };
    setFill.Parameters.Add(new ParameterDefinition(fillType));
    var getX = new MethodReference("get_x", module.TypeSystem.Single, gObject) { HasThis = true };
    var getY = new MethodReference("get_y", module.TypeSystem.Single, gObject) { HasThis = true };
    var getWidth = new MethodReference("get_width", module.TypeSystem.Single, gObject) { HasThis = true };
    var getHeight = new MethodReference("get_height", module.TypeSystem.Single, gObject) { HasThis = true };
    var getGroup = new MethodReference("get_group", gGroup, gObject) { HasThis = true };
    var setGroup = new MethodReference("set_group", module.TypeSystem.Void, gObject) { HasThis = true };
    setGroup.Parameters.Add(new ParameterDefinition(gGroup));
    var setXY = new MethodReference("SetXY", module.TypeSystem.Void, gObject) { HasThis = true };
    setXY.Parameters.Add(new ParameterDefinition(module.TypeSystem.Single));
    setXY.Parameters.Add(new ParameterDefinition(module.TypeSystem.Single));
    var setSize = new MethodReference("SetSize", module.TypeSystem.Void, gObject) { HasThis = true };
    setSize.Parameters.Add(new ParameterDefinition(module.TypeSystem.Single));
    setSize.Parameters.Add(new ParameterDefinition(module.TypeSystem.Single));
    setSize.Parameters.Add(new ParameterDefinition(module.TypeSystem.Boolean));
    var addChildAt = new MethodReference("AddChildAt", gObject, gComponent) { HasThis = true };
    addChildAt.Parameters.Add(new ParameterDefinition(gObject));
    addChildAt.Parameters.Add(new ParameterDefinition(module.TypeSystem.Int32));
    var selfField = fui.Fields.Single(field => field.Name == "self");

    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(streamingPath)));
    il.Append(Instruction.Create(OpCodes.Ldstr, "CharacterBackgrounds"));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(combine)));
    il.Append(Instruction.Create(OpCodes.Ldloca, id));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(intToString)));
    il.Append(Instruction.Create(OpCodes.Ldstr, ".png"));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(concat)));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(combine)));
    il.Append(Instruction.Create(OpCodes.Stloc, path));
    il.Append(Instruction.Create(OpCodes.Ldloc, path));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(exists)));
    il.Append(Instruction.Create(OpCodes.Brfalse, missingFile));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 4)); // TextureFormat.RGBA32
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0)); // mipChain
    il.Append(Instruction.Create(OpCodes.Newobj, module.ImportReference(textureCtor)));
    il.Append(Instruction.Create(OpCodes.Stloc, texture));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, texture));
    il.Append(Instruction.Create(OpCodes.Stfld, backgroundTexture));
    il.Append(Instruction.Create(OpCodes.Ldloc, texture));
    il.Append(Instruction.Create(OpCodes.Ldloc, path));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(readAllBytes)));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Call, module.ImportReference(loadImage)));
    il.Append(Instruction.Create(OpCodes.Brtrue, imageLoaded));
    il.Append(Instruction.Create(OpCodes.Br, loadFailed));
    il.Append(imageLoaded);

    // Use a dedicated static loader above the package movie clip. The original
    // background remains untouched and becomes visible again when this loader
    // is disposed after unequipping the background item.
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Newobj, module.ImportReference(loaderCtor)));
    il.Append(Instruction.Create(OpCodes.Stfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setTouchable)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_4)); // FillType.ScaleFree
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setFill)));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, uiField));
    il.Append(Instruction.Create(OpCodes.Ldfld, bgField));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getX)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, uiField));
    il.Append(Instruction.Create(OpCodes.Ldfld, bgField));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getY)));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setXY)));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, uiField));
    il.Append(Instruction.Create(OpCodes.Ldfld, bgField));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getWidth)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, uiField));
    il.Append(Instruction.Create(OpCodes.Ldfld, bgField));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getHeight)));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setSize)));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, uiField));
    il.Append(Instruction.Create(OpCodes.Ldfld, selfField));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(addChildAt)));
    il.Append(Instruction.Create(OpCodes.Pop));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, uiField));
    il.Append(Instruction.Create(OpCodes.Ldfld, bgField));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(getGroup)));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setGroup)));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldfld, backgroundLoader));
    il.Append(Instruction.Create(OpCodes.Ldloc, texture));
    il.Append(Instruction.Create(OpCodes.Newobj, module.ImportReference(nTextureCtor)));
    il.Append(Instruction.Create(OpCodes.Callvirt, module.ImportReference(setTexture)));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, id));
    il.Append(Instruction.Create(OpCodes.Stfld, backgroundId));
    il.Append(Instruction.Create(OpCodes.Br, done));

    il.Append(noSlot);
    il.Append(Instruction.Create(OpCodes.Br, clearLabel));
    il.Append(invalidData);
    il.Append(Instruction.Create(OpCodes.Br, clearLabel));
    il.Append(invalidType);
    il.Append(Instruction.Create(OpCodes.Br, clearLabel));
    il.Append(invalidId);
    il.Append(Instruction.Create(OpCodes.Br, clearLabel));
    il.Append(missingFile);
    il.Append(Instruction.Create(OpCodes.Br, clearLabel));
    il.Append(loadFailed);
    il.Append(Instruction.Create(OpCodes.Br, clearLabel));
    il.Append(clearLabel);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Call, clear));
    il.Append(Instruction.Create(OpCodes.Br, done));
    il.Append(done);
    il.Append(Instruction.Create(OpCodes.Ret));
    return method;
}

static void PatchDestroy(TypeDefinition characterUI, MethodDefinition clear)
{
    var destroy = characterUI.Methods.Single(method => method.Name == "Destroy" && method.Parameters.Count == 0);
    if (destroy.Body.Instructions.Any(instruction => instruction.Operand is MethodReference reference
        && reference.Name == clear.Name))
        return;
    var il = destroy.Body.GetILProcessor();
    il.InsertBefore(destroy.Body.Instructions[0], Instruction.Create(OpCodes.Ldarg_0));
    il.InsertAfter(destroy.Body.Instructions[0], Instruction.Create(OpCodes.Call, clear));
}

static void PatchWornEvent(ModuleDefinition module, TypeDefinition characterUI, MethodDefinition refresh)
{
    var stateMachine = module.Types.SelectMany(AllNestedTypes)
        .Single(type => type.FullName == "ET.UpdateWornEquipUIEvent/<Run>d__0");
    var moveNext = stateMachine.Methods.Single(method => method.Name == "MoveNext");
    if (moveNext.Body.Instructions.Any(instruction => instruction.Operand is MethodReference reference
        && reference.Name == refresh.Name))
        return;
    var fui = module.Types.Single(type => type.FullName == "ET.FUI_CharacterUI");
    var current = moveNext.Body.Variables.First(variable => variable.VariableType.FullName == fui.FullName);
    var owner = new VariableDefinition(characterUI);
    moveNext.Body.Variables.Add(owner);
    var skip = Instruction.Create(OpCodes.Nop);
    var call = FindGetParentCall(module);
    var il = moveNext.Body.GetILProcessor();
    var result = moveNext.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference reference && reference.Name == "SetResult");
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, current));
    il.InsertBefore(result, Instruction.Create(OpCodes.Brfalse, skip));
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, current));
    il.InsertBefore(result, Instruction.Create(OpCodes.Call, call));
    il.InsertBefore(result, Instruction.Create(OpCodes.Stloc, owner));
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, owner));
    il.InsertBefore(result, Instruction.Create(OpCodes.Brfalse, skip));
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldloc, owner));
    il.InsertBefore(result, Instruction.Create(OpCodes.Callvirt, module.ImportReference(refresh)));
    il.InsertBefore(result, skip);
}

static void PatchCharacterRefresh(TypeDefinition characterUI, MethodDefinition refresh)
{
    var method = characterUI.Methods.Single(candidate => candidate.Name == "Refresh"
        && candidate.Parameters.Count == 0);
    if (method.Body.Instructions.Any(instruction => instruction.Operand is MethodReference reference
        && reference.Name == refresh.Name))
        return;
    var ret = method.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Ret);
    var il = method.Body.GetILProcessor();
    il.InsertBefore(ret, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(ret, Instruction.Create(OpCodes.Call, refresh));
}

static void PatchCharacterAwakeAsync(TypeDefinition characterUI, MethodDefinition refresh)
{
    var state = characterUI.NestedTypes.Single(type => type.Name == "<AwakeAsync>d__14");
    var ownerField = state.Fields.Single(field => field.Name == "<>4__this");
    var moveNext = state.Methods.Single(method => method.Name == "MoveNext");
    if (moveNext.Body.Instructions.Any(instruction => instruction.Operand is MethodReference reference
        && reference.Name == refresh.Name))
        return;

    var result = moveNext.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Call
        && instruction.Operand is MethodReference reference && reference.Name == "SetResult");
    var il = moveNext.Body.GetILProcessor();
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(result, Instruction.Create(OpCodes.Ldfld, ownerField));
    il.InsertBefore(result, Instruction.Create(OpCodes.Callvirt, refresh));
}

static MethodReference FindGetParentCall(ModuleDefinition module)
{
    foreach (var method in module.Types.SelectMany(AllMethods))
    foreach (var instruction in method.Body?.Instructions ?? Enumerable.Empty<Instruction>())
    {
        if (instruction.Operand is GenericInstanceMethod generic && generic.Name == "GetParent")
        {
            var copy = new GenericInstanceMethod(generic.ElementMethod);
            copy.GenericArguments.Add(module.Types.Single(type => type.FullName == "ET.CharacterUI"));
            return module.ImportReference(copy);
        }
    }
    throw new InvalidOperationException("Entity.GetParent<T> reference not found");
}

static MethodReference FindTryGetValue(TypeDefinition characterUI)
{
    foreach (var method in AllMethods(characterUI))
    foreach (var instruction in method.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        if (instruction.Operand is MethodReference reference && reference.Name == "TryGetValue")
            return reference;
    throw new InvalidOperationException("WornEquipDic.TryGetValue reference not found");
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

static MethodReference FindMethod(ModuleDefinition module, string declaringType, string name)
{
    foreach (var method in module.Types.SelectMany(AllMethods))
    foreach (var instruction in method.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        if (instruction.Operand is MethodReference reference && reference.Name == name
            && reference.DeclaringType.FullName == declaringType)
            return reference;
    throw new InvalidOperationException($"method reference {declaringType}::{name} not found");
}

static TypeReference FindType(ModuleDefinition module, string ns, string name, string assembly,
    bool isValueType = false)
{
    var scope = module.AssemblyReferences.FirstOrDefault(reference => reference.Name == assembly);
    if (scope == null)
    {
        scope = new AssemblyNameReference(assembly, new Version(0, 0, 0, 0));
        module.AssemblyReferences.Add(scope);
    }
    return new TypeReference(ns, name, module, scope, isValueType);
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
