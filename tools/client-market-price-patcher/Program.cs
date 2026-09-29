using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 3)
    throw new ArgumentException("usage: ClientMarketPricePatcher <hotfix|view|coin-format> <input.dll> <output.dll>");

var mode = args[0].ToLowerInvariant();
var input = Path.GetFullPath(args[1]);
var output = Path.GetFullPath(args[2]);
var resolver = new NoResolveAssemblyResolver();
using var module = ModuleDefinition.ReadModule(input, new ReaderParameters
{
    ReadSymbols = false,
    InMemory = true,
    AssemblyResolver = resolver,
});

if (mode == "hotfix")
    PatchHotfix(module);
else if (mode == "view")
    PatchHotfixView(module);
else if (mode == "coin-format")
    PatchCoinFormat(module);
else if (mode == "inspect")
{
    Console.WriteLine("Assembly=" + module.Assembly.Name.FullName);
    Console.WriteLine("Refs=" + string.Join(",", module.AssemblyReferences.Select(r => r.FullName)));
    var inspectMethods = new HashSet<string>
    {
        "LoadCatalog", "RegisterShopRefresh", "RefreshShop", "FormatCoinPrice", "GetPriceText",
        "ShowItems", "RefreshFromServer", "FilterServerCatalog", "Destroy", "MoveNext",
        "GetAdditionalPrice", "<ShowItems>b__0",
        "SendPutOutProto",
    };
    foreach (var type in AllTypes(module).Where(t =>
        t.FullName == "ET.ServerShopPriceCache" ||
        t.FullName == "ET.ShopUI" ||
        t.FullName == "ET.TabHelper" ||
        t.FullName == "ET.StoreUI" ||
        t.FullName.Contains("ShopUI/<>c__DisplayClass4_0") ||
        t.FullName.Contains("M2C_SendActiveInfoHandler/<Run>d__0")))
        foreach (var method in type.Methods.Where(m => inspectMethods.Contains(m.Name) ||
            m.Name.Contains("ShopPrice") || m.Name.Contains("Visible") || m.Name.Contains("Trace")))
        {
            Console.WriteLine(method.FullName);
            if (method.Body != null)
                foreach (var instruction in method.Body.Instructions)
                    Console.WriteLine("  {0} {1}", instruction.OpCode, instruction.Operand);
        }
    return;
}
else
    throw new ArgumentException($"unknown mode: {mode}");

ValidateBranchTargets(module);
resolver.Populate(module);
Directory.CreateDirectory(Path.GetDirectoryName(output)!);
module.Write(output, new WriterParameters { WriteSymbols = false });
Console.WriteLine($"patched {mode}: {output}");

static void ValidateBranchTargets(ModuleDefinition module)
{
    // Cecil accepts an Instruction used as a branch target even when it was
    // never appended to the method body. The writer then resolves that
    // dangling object to an unrelated offset (in the old catalog hook, the
    // loop exit became the method entry and spun forever). Fail during patch
    // generation instead of producing a client that hangs at startup.
    foreach (var method in AllTypes(module).SelectMany(t => t.Methods))
    {
        if (method.Body == null)
            continue;
        var instructions = method.Body.Instructions.ToHashSet();
        foreach (var instruction in method.Body.Instructions)
        {
            if (instruction.Operand is Instruction target && !instructions.Contains(target))
                throw new InvalidOperationException($"{method.FullName}: dangling branch target");
            if (instruction.Operand is Instruction[] targets && targets.Any(target => !instructions.Contains(target)))
                throw new InvalidOperationException($"{method.FullName}: dangling switch target");
        }
    }
}

static void PatchHotfix(ModuleDefinition module)
{
    // Keep the original client's imported generic List<T> method references
    // available while we emit the cache.  Unity's Mono runtime is stricter
    // than CoreCLR when resolving a hand-created reference to List<int>.
    // Reusing a reference that already exists in the input assembly preserves
    // the exact generic-instance metadata emitted by the Unity compiler.
    PatchContext.Module = module;
    var market = FindType(module, "ET.M2C_GetMarket");
    var active = FindType(module, "ET.M2C_SendActiveInfo");
    var marketIds = FindField(market, "MarketIdList");
    var listInt = marketIds.FieldType;
    AddProtoField(module, market, "PriceList", listInt, marketIds, 6);
    var gotSignIn = FindField(active, "gotSignInMonthList");
    AddProtoField(module, active, "ShopPriceData", gotSignIn.FieldType, gotSignIn, 6);
    AddProtoField(module, active, "ShopDescriptionData", StringListType(module, gotSignIn.FieldType), gotSignIn, 7);
    var netItem = FindType(module, "ET.NetItem");
    var getSource = FindField(netItem, "<GetSource>k__BackingField");
    AddProtoField(module, netItem, "<Description>k__BackingField", module.TypeSystem.String, getSource, 7);
    var marketMessage = FindType(module, "ET.M2C_GetMarket");
    var marketMessageIds = FindField(marketMessage, "MarketIdList");
    AddProtoField(module, marketMessage, "DescriptionList", StringListType(module, marketMessageIds.FieldType),
        marketMessageIds, 7);

    var cache = AllTypes(module).SingleOrDefault(t => t.FullName == "ET.ServerShopPriceCache");
    if (cache == null)
        cache = BuildCache(module, listInt);
    else if (!cache.Methods.Any(m => m.Name == "RegisterShopRefresh"))
        BuildShopRefreshCallbacks(module, cache, ExistingActionType(module));

    var loadCatalog = cache.Methods.Single(m => m.Name == "LoadCatalog");
    InjectActiveInfoHandler(module, loadCatalog);
    // BuildFormatMethods historically emitted the raw ShopBase integer with
    // a literal "金币" suffix.  Once the cache already existed, rerunning
    // the combined installer skipped BuildCache and therefore kept that
    // stale formatter forever.  Always replace it here so an install from
    // any previously patched DLL converges to the gold/silver/copper format.
    var coinFormat = cache.Methods.Single(m => m.Name == "FormatCoinPrice" &&
        m.Parameters.Count == 1 && m.Parameters[0].ParameterType.FullName == "System.Int32");
    ReplaceCoinFormatBody(module, cache, coinFormat);
    Console.WriteLine("Hotfix: added M2C_GetMarket.PriceList and M2C_SendActiveInfo.ShopPriceData");
}

static void PatchHotfixView(ModuleDefinition module)
{
    PatchContext.Module = module;
    var cacheType = ExternalCacheType(module);
    var getPriceText = ExternalMethod(module, cacheType, "GetPriceText",
        module.TypeSystem.String, module.TypeSystem.Object, module.TypeSystem.Int32);
    var tabHelper = FindType(module, "ET.TabHelper");
    var priceMethod = tabHelper.Methods.Single(m => m.Name == "GetAdditionalPrice" &&
        m.Parameters.Count == 3 && m.Parameters[0].ParameterType.FullName == "ET.IConfig");
    InjectPriceOverride(priceMethod, getPriceText);
    var multiMethod = tabHelper.Methods.SingleOrDefault(m => m.Name == "GetAdditionalPrice" &&
        m.Parameters.Count == 1 && m.Parameters[0].ParameterType.FullName == "Cal.DataTable.MultiShop");
    if (multiMethod != null)
        InjectPriceOverride(multiMethod, getPriceText, false);
    InjectMarketShowItems(module, cacheType);
    InjectShopRefreshRegistration(module, cacheType);
    InjectShopFilters(module, cacheType);
    InjectBagDescriptionApply(module, cacheType);
    Console.WriteLine("HotfixView: server shop prices override local tooltip prices with fallback");
}

static void PatchCoinFormat(ModuleDefinition module)
{
    var cache = FindType(module, "ET.ServerShopPriceCache");
    var method = cache.Methods.Single(m => m.Name == "FormatCoinPrice" &&
        m.Parameters.Count == 1 && m.Parameters[0].ParameterType.FullName == "System.Int32");
    ReplaceCoinFormatBody(module, cache, method);
    Console.WriteLine("Hotfix: formatted ordinary-shop prices as gold, silver, and copper");
}

static void ReplaceCoinFormatBody(ModuleDefinition module, TypeDefinition cache, MethodDefinition method)
{
    var formatInt = cache.Methods.Single(m => m.Name == "FormatInt" &&
        m.Parameters.Count == 1 && m.Parameters[0].ParameterType.FullName == "System.Int32");
    var concat = FindExistingMethod(module, "System.String", "Concat", 2) ??
        throw new InvalidOperationException("System.String.Concat(string,string) reference missing");
    if (concat.Parameters.Any(p => p.ParameterType.FullName != "System.String"))
        throw new InvalidOperationException("selected String.Concat overload is not (string,string)");

    method.Body = new MethodBody(method) { InitLocals = true };
    var il = method.Body.GetILProcessor();
    var gold = NewVariable(method, module.TypeSystem.Int32);
    var silver = NewVariable(method, module.TypeSystem.Int32);
    var copper = NewVariable(method, module.TypeSystem.Int32);

    // Price is stored and transmitted in the smallest copper unit. Match the
    // original TabHelper.GetCoinFormat thresholds: 100 copper = 1 silver,
    // 10000 copper = 1 gold. Keep all three fields in the tooltip, including
    // zero values, because the original client renders all three coin icons.
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 10000));
    il.Append(Instruction.Create(OpCodes.Div));
    il.Append(Instruction.Create(OpCodes.Stloc, gold));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 10000));
    il.Append(Instruction.Create(OpCodes.Rem));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 100));
    il.Append(Instruction.Create(OpCodes.Div));
    il.Append(Instruction.Create(OpCodes.Stloc, silver));

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 100));
    il.Append(Instruction.Create(OpCodes.Rem));
    il.Append(Instruction.Create(OpCodes.Stloc, copper));

    il.Append(Instruction.Create(OpCodes.Ldstr, "\n  现价:    "));
    AppendCoinPart(il, gold, "[img]ui://kqsmrpxleh2aa[/img]", formatInt, concat);
    il.Append(Instruction.Create(OpCodes.Ldstr, " "));
    il.Append(Instruction.Create(OpCodes.Call, concat));
    AppendCoinPart(il, silver, "[img]ui://kqsmrpxleh2ab[/img]", formatInt, concat);
    il.Append(Instruction.Create(OpCodes.Ldstr, " "));
    il.Append(Instruction.Create(OpCodes.Call, concat));
    AppendCoinPart(il, copper, "[img]ui://kqsmrpxleh2am[/img]", formatInt, concat);
    il.Append(Instruction.Create(OpCodes.Ret));
}

static void AppendCoinPart(ILProcessor il, VariableDefinition value, string icon,
    MethodDefinition formatInt, MethodReference concat)
{
    il.Append(Instruction.Create(OpCodes.Ldloc, value));
    il.Append(Instruction.Create(OpCodes.Call, formatInt));
    il.Append(Instruction.Create(OpCodes.Call, concat));
    il.Append(Instruction.Create(OpCodes.Ldstr, icon));
    il.Append(Instruction.Create(OpCodes.Call, concat));
}

static void InjectShopRefreshRegistration(ModuleDefinition module, TypeReference cacheType)
{
    var shop = FindType(module, "ET.ShopUI");
    var show = shop.Methods.SingleOrDefault(m => m.Name == "ShowItems");
    if (show?.Body == null)
        return;
    var pageField = shop.Fields.FirstOrDefault(f => f.Name == "serverRefreshPage");
    if (pageField == null)
    {
        pageField = new FieldDefinition("serverRefreshPage", FieldAttributes.Private,
            module.TypeSystem.Int32);
        shop.Fields.Add(pageField);
    }
    var refresh = shop.Methods.FirstOrDefault(m => m.Name == "RefreshFromServer");
    if (refresh == null)
    {
        refresh = new MethodDefinition("RefreshFromServer",
            MethodAttributes.Private | MethodAttributes.HideBySig, module.TypeSystem.Void);
        var body = refresh.Body.GetILProcessor();
        body.Append(Instruction.Create(OpCodes.Ldarg_0));
        body.Append(Instruction.Create(OpCodes.Ldarg_0));
        body.Append(Instruction.Create(OpCodes.Ldfld, pageField));
        body.Append(Instruction.Create(OpCodes.Call, show));
        body.Append(Instruction.Create(OpCodes.Ret));
        shop.Methods.Add(refresh);
    }
    var register = ExternalMethod(module, cacheType, "RegisterShopRefresh", module.TypeSystem.Void,
        ExistingActionType(module));
    var actionCtor = FindActionConstructor(module);
    if (!show.Body.Instructions.Any(i => i.Operand is MethodReference mr &&
        mr.Name == "RegisterShopRefresh"))
    {
        var il = show.Body.GetILProcessor();
        var first = show.Body.Instructions.First();
        // Keep the selected page in the UI entity and replace the callback
        // each time ShowItems runs.  This also covers page switches after a
        // reload.
        il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
        il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_1));
        il.InsertBefore(first, Instruction.Create(OpCodes.Stfld, pageField));
        il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
        il.InsertBefore(first, Instruction.Create(OpCodes.Ldftn, refresh));
        il.InsertBefore(first, Instruction.Create(OpCodes.Newobj, actionCtor));
        il.InsertBefore(first, Instruction.Create(OpCodes.Call, register));
    }

    // Clear the callback when the entity is destroyed; otherwise a later
    // hot-reload could invoke a disposed window instance.
    var destroy = shop.Methods.SingleOrDefault(m => m.Name == "Destroy");
    if (destroy?.Body != null && !destroy.Body.Instructions.Any(i =>
        i.Operand is MethodReference mr && mr.Name == "RegisterShopRefresh"))
    {
        var destroyFirst = destroy.Body.Instructions.First();
        var nil = Instruction.Create(OpCodes.Ldnull);
        var destroyIL = destroy.Body.GetILProcessor();
        destroyIL.InsertBefore(destroyFirst, nil);
        destroyIL.InsertBefore(destroyFirst, Instruction.Create(OpCodes.Call, register));
    }
}

static MethodReference FindActionConstructor(ModuleDefinition module)
{
    var ctor = AllMethods(module).SelectMany(m => m.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        .Select(i => i.Operand).OfType<MethodReference>()
        .FirstOrDefault(m => m.Name == ".ctor" && m.DeclaringType.FullName == "System.Action" &&
            m.Parameters.Count == 2);
    if (ctor != null)
        return ctor;
    var action = ExistingActionType(module);
    var fallback = new MethodReference(".ctor", module.TypeSystem.Void, action) { HasThis = true };
    fallback.Parameters.Add(new ParameterDefinition(module.TypeSystem.Object));
    fallback.Parameters.Add(new ParameterDefinition(module.TypeSystem.IntPtr));
    return fallback;
}

static void AddProtoField(ModuleDefinition module, TypeDefinition type, string name,
    TypeReference fieldType, FieldDefinition source, int tag)
{
    if (type.Fields.Any(f => f.Name == name))
        return;
    var field = new FieldDefinition(name, FieldAttributes.Public, module.ImportReference(fieldType));
    var protoAttribute = source.CustomAttributes.FirstOrDefault(a =>
        a.AttributeType.FullName == "ProtoBuf.ProtoMemberAttribute");
    if (protoAttribute != null)
    {
        var attr = new CustomAttribute(module.ImportReference(protoAttribute.Constructor));
        foreach (var arg in protoAttribute.ConstructorArguments)
            attr.ConstructorArguments.Add(arg);
        if (attr.ConstructorArguments.Count > 0)
            attr.ConstructorArguments[0] = new CustomAttributeArgument(module.TypeSystem.Int32, tag);
        foreach (var named in protoAttribute.Fields)
            attr.Fields.Add(new CustomAttributeNamedArgument(named.Name, named.Argument));
        foreach (var named in protoAttribute.Properties)
            attr.Properties.Add(new CustomAttributeNamedArgument(named.Name, named.Argument));
        field.CustomAttributes.Add(attr);
    }
    else
    {
        var protoType = module.GetTypeReferences().First(t => t.FullName == "ProtoBuf.ProtoMemberAttribute");
        var ctor = new MethodReference(".ctor", module.TypeSystem.Void, protoType) { HasThis = true };
        ctor.Parameters.Add(new ParameterDefinition(module.TypeSystem.Int32));
        var attr = new CustomAttribute(ctor);
        attr.ConstructorArguments.Add(new CustomAttributeArgument(module.TypeSystem.Int32, tag));
        field.CustomAttributes.Add(attr);
    }
    type.Fields.Add(field);
}

static TypeDefinition BuildCache(ModuleDefinition module, TypeReference listInt)
{
    var cache = new TypeDefinition("ET", "ServerShopPriceCache",
        TypeAttributes.Public | TypeAttributes.Abstract | TypeAttributes.Sealed | TypeAttributes.BeforeFieldInit,
        module.TypeSystem.Object);
    module.Types.Add(cache);
    var intArray = new ArrayType(module.TypeSystem.Int32);
    var marketIds = AddStaticField(cache, "marketIds", intArray);
    var marketYuanbao = AddStaticField(cache, "marketYuanbao", intArray);
    var marketVoucher = AddStaticField(cache, "marketVoucher", intArray);
    var shopIds = AddStaticField(cache, "shopIds", intArray);
    var shopPrices = AddStaticField(cache, "shopPrices", intArray);
    var multiIds = AddStaticField(cache, "multiIds", intArray);
    var multiTypes = AddStaticField(cache, "multiTypes", intArray);
    var multiPrices = AddStaticField(cache, "multiPrices", intArray);
    var marketCount = AddStaticField(cache, "marketCount", module.TypeSystem.Int32);
    var shopCount = AddStaticField(cache, "shopCount", module.TypeSystem.Int32);
    var multiCount = AddStaticField(cache, "multiCount", module.TypeSystem.Int32);
    var catalogLoaded = AddStaticField(cache, "catalogLoaded", module.TypeSystem.Boolean);
    // The ordinary shop window is a long-lived UI entity.  Keep a tiny
    // callback supplied by HotfixView so a catalog hot-reload can redraw an
    // already-open window instead of waiting for the player to close/reopen it.
    var actionType = ExistingActionType(module);
    AddStaticField(cache, "shopRefresh", actionType);
    BuildCacheConstructor(module, cache, intArray,
        new[] { marketIds, marketYuanbao, marketVoucher, shopIds, shopPrices,
            multiIds, multiTypes, multiPrices });
    BuildLoadMarket(module, cache, listInt, marketIds, marketYuanbao, marketVoucher, marketCount);
    BuildApplyDescription(module, cache);
    BuildApplyCatalogDescriptions(module, cache, listInt, StringListType(module, listInt));
    BuildApplyMarketDescriptions(module, cache, listInt, StringListType(module, listInt));
    BuildLoadCatalog(module, cache, listInt, marketIds, marketYuanbao, marketVoucher,
        shopIds, shopPrices, multiIds, multiTypes, multiPrices,
        marketCount, shopCount, multiCount);
    BuildShopRefreshCallbacks(module, cache, actionType);
    BuildFindMethod(module, cache, "GetMarketPrice", marketIds, marketYuanbao,
        null, false, marketCount);
    BuildFindMethod(module, cache, "GetVoucherPrice", marketIds, marketVoucher,
        null, false, marketCount);
    BuildFindMethod(module, cache, "GetShopPrice", shopIds, shopPrices,
        null, false, shopCount);
    BuildFindMethod(module, cache, "GetMultiPrice", multiIds, multiPrices,
        multiTypes, true, multiCount);
    BuildVisibilityMethod(module, cache, "IsShopItemVisible", catalogLoaded, shopIds, shopPrices,
        false);
    BuildVisibilityMethod(module, cache, "IsMultiItemVisible", catalogLoaded, multiIds, multiPrices,
        true, multiTypes);
    BuildFormatMethods(module, cache);
    BuildGetPriceText(module, cache);
    return cache;
}

static FieldDefinition AddStaticField(TypeDefinition type, string name, TypeReference fieldType)
{
    var field = new FieldDefinition(name, FieldAttributes.Private | FieldAttributes.Static, fieldType);
    type.Fields.Add(field);
    return field;
}

static void BuildCacheConstructor(ModuleDefinition module, TypeDefinition cache,
    ArrayType intArray, FieldDefinition[] fields)
{
    var cctor = new MethodDefinition(".cctor", MethodAttributes.Private | MethodAttributes.Static |
        MethodAttributes.SpecialName | MethodAttributes.RTSpecialName, module.TypeSystem.Void);
    var il = cctor.Body.GetILProcessor();
    foreach (var field in fields)
    {
        il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
        il.Append(Instruction.Create(OpCodes.Newarr, intArray.ElementType));
        il.Append(Instruction.Create(OpCodes.Stsfld, field));
    }
    il.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(cctor);
}

static void BuildLoadMarket(ModuleDefinition module, TypeDefinition cache, TypeReference listInt,
    FieldDefinition ids, FieldDefinition yuanbao, FieldDefinition voucher, FieldDefinition countField)
{
    var method = NewStatic(cache, "LoadMarket", module.TypeSystem.Void,
        ("ids", listInt), ("prices", listInt));
    var il = method.Body.GetILProcessor();
    var empty = new[] { ids, yuanbao, voucher };
    foreach (var field in empty)
    {
        il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
        il.Append(Instruction.Create(OpCodes.Newarr, module.TypeSystem.Int32));
        il.Append(Instruction.Create(OpCodes.Stsfld, field));
    }
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stsfld, countField));
    var done = Instruction.Create(OpCodes.Ret);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    var index = NewVariable(method, module.TypeSystem.Int32);
    var count = NewVariable(method, module.TypeSystem.Int32);
    var priceCount = NewVariable(method, module.TypeSystem.Int32);
    var loop = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    var noPrices = Instruction.Create(OpCodes.Ret);
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Brfalse, noPrices));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Div));
    il.Append(Instruction.Create(OpCodes.Stloc, priceCount));
    var countReady = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldloc, priceCount));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, countReady));
    il.Append(Instruction.Create(OpCodes.Ldloc, priceCount));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    il.Append(countReady);
    foreach (var field in empty)
    {
        il.Append(Instruction.Create(OpCodes.Ldloc, count));
        il.Append(Instruction.Create(OpCodes.Newarr, module.TypeSystem.Int32));
        il.Append(Instruction.Create(OpCodes.Stsfld, field));
    }
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Stsfld, countField));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(loop);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, done));
    il.Append(Instruction.Create(OpCodes.Ldsfld, ids));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Item", module.TypeSystem.Int32,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stelem_I4));
    il.Append(Instruction.Create(OpCodes.Ldsfld, yuanbao));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Mul));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Item", module.TypeSystem.Int32,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stelem_I4));
    il.Append(Instruction.Create(OpCodes.Ldsfld, voucher));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Mul));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Item", module.TypeSystem.Int32,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stelem_I4));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(Instruction.Create(OpCodes.Br, loop));
    il.Append(done);
    il.Append(noPrices);
    cache.Methods.Add(method);
}

static GenericInstanceType StringListType(ModuleDefinition module, TypeReference listType)
{
    if (listType is GenericInstanceType generic && generic.GenericArguments.Count == 1)
    {
        var result = new GenericInstanceType(module.ImportReference(generic.ElementType));
        result.GenericArguments.Add(module.TypeSystem.String);
        return result;
    }
    throw new InvalidOperationException($"expected generic List<T> field, got {listType.FullName}");
}

static void BuildApplyDescription(ModuleDefinition module, TypeDefinition cache)
{
    if (cache.Methods.Any(m => m.Name == "ApplyDescription"))
        return;
    var method = NewStatic(cache, "ApplyDescription", module.TypeSystem.Void,
        ("itemId", module.TypeSystem.Int32), ("description", module.TypeSystem.String));
    var il = method.Body.GetILProcessor();
    var done = Instruction.Create(OpCodes.Ret);
    var goods = Instruction.Create(OpCodes.Nop);
    var equip = Instruction.Create(OpCodes.Nop);
    var target = NewVariable(method, module.TypeSystem.Object);
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Call, StringIsNullOrWhiteSpace(module)));
    il.Append(Instruction.Create(OpCodes.Brtrue, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 120000));
    il.Append(Instruction.Create(OpCodes.Bge, equip));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, 110000));
    il.Append(Instruction.Create(OpCodes.Bge, goods));
    AppendGetAndSetDescription(il, module, FindDataTableGet(module, "Cal.DataTable.MaterialBase"),
        FindType(module, "Cal.DataTable.MaterialBase"));
    il.Append(Instruction.Create(OpCodes.Br, done));
    il.Append(goods);
    AppendGetAndSetDescription(il, module, FindDataTableGet(module, "Cal.DataTable.GoodsBase"),
        FindType(module, "Cal.DataTable.GoodsBase"));
    il.Append(Instruction.Create(OpCodes.Br, done));
    il.Append(equip);
    AppendGetAndSetDescription(il, module, FindDataTableGet(module, "Cal.DataTable.EquipBase"),
        FindType(module, "Cal.DataTable.EquipBase"));
    il.Append(done);
    cache.Methods.Add(method);
}

static void BuildApplyCatalogDescriptions(ModuleDefinition module, TypeDefinition cache,
    TypeReference listInt, TypeReference listString)
{
    if (cache.Methods.Any(m => m.Name == "ApplyCatalogDescriptions"))
        return;
    var method = NewStatic(cache, "ApplyCatalogDescriptions", module.TypeSystem.Void,
        ("data", listInt), ("descriptions", listString));
    var il = method.Body.GetILProcessor();
    var done = Instruction.Create(OpCodes.Ret);
    var index = NewVariable(method, module.TypeSystem.Int32);
    var count = NewVariable(method, module.TypeSystem.Int32);
    var descCount = NewVariable(method, module.TypeSystem.Int32);
    var itemId = NewVariable(method, module.TypeSystem.Int32);
    var description = NewVariable(method, module.TypeSystem.String);
    var loop = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_7));
    il.Append(Instruction.Create(OpCodes.Div));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listString, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, descCount));
    var countReady = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldloc, descCount));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, countReady));
    il.Append(Instruction.Create(OpCodes.Ldloc, descCount));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    il.Append(countReady);
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(loop);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, done));
    // item id is the fifth integer in each seven-field catalog record.
    LoadDataIntStride(il, module, listInt, index, 7, 4, itemId);
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listString, "get_Item", module.TypeSystem.String,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, description));
    il.Append(Instruction.Create(OpCodes.Ldloc, itemId));
    il.Append(Instruction.Create(OpCodes.Ldloc, description));
    il.Append(Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "ApplyDescription")));
    IncrementLocal(il, index);
    il.Append(Instruction.Create(OpCodes.Br, loop));
    il.Append(done);
    cache.Methods.Add(method);
}

static void BuildApplyMarketDescriptions(ModuleDefinition module, TypeDefinition cache,
    TypeReference listInt, TypeReference listString)
{
    if (cache.Methods.Any(m => m.Name == "ApplyMarketDescriptions"))
        return;
    var method = NewStatic(cache, "ApplyMarketDescriptions", module.TypeSystem.Void,
        ("ids", listInt), ("descriptions", listString));
    var il = method.Body.GetILProcessor();
    var done = Instruction.Create(OpCodes.Ret);
    var index = NewVariable(method, module.TypeSystem.Int32);
    var count = NewVariable(method, module.TypeSystem.Int32);
    var descCount = NewVariable(method, module.TypeSystem.Int32);
    var itemId = NewVariable(method, module.TypeSystem.Int32);
    var description = NewVariable(method, module.TypeSystem.String);
    var loop = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Brfalse, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listString, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, descCount));
    var countReady = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldloc, descCount));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, countReady));
    il.Append(Instruction.Create(OpCodes.Ldloc, descCount));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    il.Append(countReady);
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(loop);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Item", module.TypeSystem.Int32,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, itemId));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listString, "get_Item", module.TypeSystem.String,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, description));
    il.Append(Instruction.Create(OpCodes.Ldloc, itemId));
    il.Append(Instruction.Create(OpCodes.Ldloc, description));
    il.Append(Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "ApplyDescription")));
    IncrementLocal(il, index);
    il.Append(Instruction.Create(OpCodes.Br, loop));
    il.Append(done);
    cache.Methods.Add(method);
}

static MethodReference StringIsNullOrWhiteSpace(ModuleDefinition module)
{
    var method = new MethodReference("IsNullOrWhiteSpace", module.TypeSystem.Boolean,
        module.TypeSystem.String) { HasThis = false };
    method.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    return method;
}

static void AppendGetAndSetDescription(ILProcessor il, ModuleDefinition module,
    MethodReference getMethod, TypeDefinition type)
{
    var description = FindField(type, "Description");
    var method = il.Body.Method;
    var target = NewVariable(method, type);
    var skip = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Conv_I4));
    il.Append(Instruction.Create(OpCodes.Call, getMethod));
    il.Append(Instruction.Create(OpCodes.Stloc, target));
    il.Append(Instruction.Create(OpCodes.Ldloc, target));
    il.Append(Instruction.Create(OpCodes.Brfalse, skip));
    il.Append(Instruction.Create(OpCodes.Ldloc, target));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Stfld, description));
    il.Append(skip);
}

static MethodReference FindDataTableGet(ModuleDefinition module, string typeName)
{
    var result = AllMethods(module).SelectMany(m => m.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        .Select(i => i.Operand).OfType<GenericInstanceMethod>()
        .FirstOrDefault(m => m.Name == "Get" && m.DeclaringType.FullName == "ET.DataTableHelper" &&
            m.GenericArguments.Count == 1 && m.GenericArguments[0].FullName == typeName);
    return result ?? throw new InvalidOperationException($"DataTableHelper.Get<{typeName}> reference missing");
}

static void BuildLoadCatalog(ModuleDefinition module, TypeDefinition cache, TypeReference listInt,
    FieldDefinition marketIds, FieldDefinition marketYuanbao, FieldDefinition marketVoucher,
    FieldDefinition shopIds, FieldDefinition shopPrices, FieldDefinition multiIds,
    FieldDefinition multiTypes, FieldDefinition multiPrices, FieldDefinition marketCount,
    FieldDefinition shopCount, FieldDefinition multiCount)
{
    var method = NewStatic(cache, "LoadCatalog", module.TypeSystem.Void, ("data", listInt));
    var il = method.Body.GetILProcessor();
    var dataNull = Instruction.Create(OpCodes.Ret);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Brfalse, dataNull));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Stsfld, cache.Fields.Single(f => f.Name == "catalogLoaded")));
    var dataCount = NewVariable(method, module.TypeSystem.Int32);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, dataCount));
    foreach (var field in new[] { marketIds, marketYuanbao, marketVoucher, shopIds,
        shopPrices, multiIds, multiTypes, multiPrices })
    {
        il.Append(Instruction.Create(OpCodes.Ldloc, dataCount));
        il.Append(Instruction.Create(OpCodes.Newarr, module.TypeSystem.Int32));
        il.Append(Instruction.Create(OpCodes.Stsfld, field));
    }
    foreach (var field in new[] { marketCount, shopCount, multiCount })
    {
        il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
        il.Append(Instruction.Create(OpCodes.Stsfld, field));
    }
    var index = NewVariable(method, module.TypeSystem.Int32);
    var count = NewVariable(method, module.TypeSystem.Int32);
    var storeType = NewVariable(method, module.TypeSystem.Int32);
    var pageOrType = NewVariable(method, module.TypeSystem.Int32);
    var itemId = NewVariable(method, module.TypeSystem.Int32);
    var price = NewVariable(method, module.TypeSystem.Int32);
    var voucher = NewVariable(method, module.TypeSystem.Int32);
    var marketIndex = NewVariable(method, module.TypeSystem.Int32);
    var shopIndex = NewVariable(method, module.TypeSystem.Int32);
    var multiIndex = NewVariable(method, module.TypeSystem.Int32);
    var loop = Instruction.Create(OpCodes.Nop);
    var end = Instruction.Create(OpCodes.Ret);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Count", module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, marketIndex));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, shopIndex));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, multiIndex));
    il.Append(loop);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_6));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, end));
    LoadDataInt(il, module, listInt, index, 0, storeType);
    LoadDataInt(il, module, listInt, index, 1, pageOrType);
    LoadDataInt(il, module, listInt, index, 4, itemId);
    LoadDataInt(il, module, listInt, index, 5, price);
    LoadDataInt(il, module, listInt, index, 6, voucher);
    var notMarket = Instruction.Create(OpCodes.Nop);
    var notShop = Instruction.Create(OpCodes.Nop);
    var after = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldloc, storeType));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Bne_Un, notMarket));
    StoreArrayValue(il, marketIds, marketIndex, itemId);
    StoreArrayValue(il, marketYuanbao, marketIndex, price);
    StoreArrayValue(il, marketVoucher, marketIndex, voucher);
    IncrementLocal(il, marketIndex);
    IncrementField(il, marketCount);
    il.Append(Instruction.Create(OpCodes.Br, after));
    il.Append(notMarket);
    il.Append(Instruction.Create(OpCodes.Ldloc, storeType));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_2));
    il.Append(Instruction.Create(OpCodes.Bne_Un, notShop));
    StoreArrayValue(il, shopIds, shopIndex, itemId);
    StoreArrayValue(il, shopPrices, shopIndex, price);
    IncrementLocal(il, shopIndex);
    IncrementField(il, shopCount);
    il.Append(Instruction.Create(OpCodes.Br, after));
    il.Append(notShop);
    il.Append(Instruction.Create(OpCodes.Ldloc, storeType));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_3));
    il.Append(Instruction.Create(OpCodes.Bne_Un, after));
    StoreArrayValue(il, multiIds, multiIndex, itemId);
    StoreArrayValue(il, multiTypes, multiIndex, pageOrType);
    StoreArrayValue(il, multiPrices, multiIndex, price);
    IncrementLocal(il, multiIndex);
    IncrementField(il, multiCount);
    il.Append(after);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_7));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(Instruction.Create(OpCodes.Br, loop));
    // ``end`` is a branch target and must be part of the method body. Leaving
    // it unattached makes Mono.Cecil encode the Bge target as the method
    // entry, so a fully consumed catalog loops forever instead of returning.
    il.Append(end);
    il.Append(dataNull);
    cache.Methods.Add(method);
}

static void BuildShopRefreshCallbacks(ModuleDefinition module, TypeDefinition cache,
    TypeReference actionType)
{
    var register = NewStatic(cache, "RegisterShopRefresh", module.TypeSystem.Void,
        ("callback", actionType));
    var registerIL = register.Body.GetILProcessor();
    registerIL.Append(Instruction.Create(OpCodes.Ldarg_0));
    registerIL.Append(Instruction.Create(OpCodes.Stsfld, cache.Fields.Single(f => f.Name == "shopRefresh")));
    registerIL.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(register);

    var refresh = NewStatic(cache, "RefreshShop", module.TypeSystem.Void);
    var refreshIL = refresh.Body.GetILProcessor();
    var callback = NewVariable(refresh, actionType);
    var done = Instruction.Create(OpCodes.Ret);
    refreshIL.Append(Instruction.Create(OpCodes.Ldsfld, cache.Fields.Single(f => f.Name == "shopRefresh")));
    refreshIL.Append(Instruction.Create(OpCodes.Stloc, callback));
    refreshIL.Append(Instruction.Create(OpCodes.Ldloc, callback));
    refreshIL.Append(Instruction.Create(OpCodes.Brfalse, done));
    refreshIL.Append(Instruction.Create(OpCodes.Ldloc, callback));
    var invoke = new MethodReference("Invoke", module.TypeSystem.Void, actionType) { HasThis = true };
    refreshIL.Append(Instruction.Create(OpCodes.Callvirt, invoke));
    refreshIL.Append(done);
    cache.Methods.Add(refresh);

    // RefreshShop is called only after all arrays have been rebuilt.  Insert
    // it immediately before LoadCatalog's normal return; the null-data early
    // return intentionally remains untouched.
    var load = cache.Methods.Single(m => m.Name == "LoadCatalog");
    var loadIL = load.Body.GetILProcessor();
    var returns = load.Body.Instructions.Where(i => i.OpCode == OpCodes.Ret).ToArray();
    if (returns.Length < 2)
        throw new InvalidOperationException("LoadCatalog return targets missing");
    var ret = returns[^2];
    loadIL.InsertBefore(ret, Instruction.Create(OpCodes.Call, refresh));
}

static TypeReference ExistingActionType(ModuleDefinition module)
{
    // Reuse the delegate type already present in the Unity-compiled assembly
    // so Mono resolves it against the game's mscorlib rather than CoreLib.
    var ctor = AllMethods(module).SelectMany(m => m.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        .Select(i => i.Operand).OfType<MethodReference>()
        .FirstOrDefault(m => m.Name == ".ctor" && m.DeclaringType.FullName == "System.Action");
    if (ctor != null)
        return ctor.DeclaringType;
    var scope = module.AssemblyReferences.FirstOrDefault(r => r.Name == "mscorlib")
        ?? throw new InvalidOperationException("mscorlib reference missing for System.Action");
    return new TypeReference("System", "Action", module, scope);
}

static void BuildVisibilityMethod(ModuleDefinition module, TypeDefinition cache, string name,
    FieldDefinition loaded, FieldDefinition ids, FieldDefinition prices,
    bool hasType, FieldDefinition? typeList = null)
{
    var method = new MethodDefinition(name, MethodAttributes.Public | MethodAttributes.Static,
        module.TypeSystem.Boolean);
    method.Parameters.Add(new ParameterDefinition("itemId", ParameterAttributes.None, module.TypeSystem.Int32));
    if (hasType)
        method.Parameters.Add(new ParameterDefinition("shopType", ParameterAttributes.None, module.TypeSystem.Int32));
    var il = method.Body.GetILProcessor();
    var loadedLabel = Instruction.Create(OpCodes.Nop);
    // The server catalog is authoritative.  Until it arrives, do not render
    // stale static ShopBase/MultiShop rows: the map-start push (or the refresh
    // callback for an already-open window) will redraw the list once loaded.
    var hiddenUntilLoaded = Instruction.Create(OpCodes.Ldc_I4_0);
    var priceMethod = cache.Methods.Single(m => m.Name == (hasType ? "GetMultiPrice" : "GetShopPrice"));
    il.Append(Instruction.Create(OpCodes.Ldsfld, loaded));
    il.Append(Instruction.Create(OpCodes.Brtrue, loadedLabel));
    il.Append(hiddenUntilLoaded);
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(loadedLabel);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    if (hasType)
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Call, priceMethod));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Clt));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Ceq));
    il.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(method);
}

static void LoadDataInt(ILProcessor il, ModuleDefinition module, TypeReference listInt,
    VariableDefinition index, int offset, VariableDefinition target)
{
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    if (offset != 0)
    {
        il.Append(Instruction.Create(OpCodes.Ldc_I4, offset));
        il.Append(Instruction.Create(OpCodes.Add));
    }
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Item", module.TypeSystem.Int32,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, target));
}

static void LoadDataIntStride(ILProcessor il, ModuleDefinition module, TypeReference listInt,
    VariableDefinition index, int stride, int offset, VariableDefinition target)
{
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldc_I4, stride));
    il.Append(Instruction.Create(OpCodes.Mul));
    if (offset != 0)
    {
        il.Append(Instruction.Create(OpCodes.Ldc_I4, offset));
        il.Append(Instruction.Create(OpCodes.Add));
    }
    il.Append(Instruction.Create(OpCodes.Callvirt, ListMethod(listInt, "get_Item", module.TypeSystem.Int32,
        module.TypeSystem.Int32)));
    il.Append(Instruction.Create(OpCodes.Stloc, target));
}

static void BuildFindMethod(ModuleDefinition module, TypeDefinition cache, string name,
    FieldDefinition ids, FieldDefinition prices, FieldDefinition? typeList, bool hasType,
    FieldDefinition countField)
{
    var method = new MethodDefinition(name, MethodAttributes.Public | MethodAttributes.Static,
        module.TypeSystem.Int32);
    method.Parameters.Add(new ParameterDefinition("itemId", ParameterAttributes.None, module.TypeSystem.Int32));
    if (hasType)
        method.Parameters.Add(new ParameterDefinition("shopType", ParameterAttributes.None, module.TypeSystem.Int32));
    var il = method.Body.GetILProcessor();
    var index = NewVariable(method, module.TypeSystem.Int32);
    var count = NewVariable(method, module.TypeSystem.Int32);
    var notFound = Instruction.Create(OpCodes.Ldc_I4_M1);
    var loop = Instruction.Create(OpCodes.Nop);
    var next = Instruction.Create(OpCodes.Nop);
    var end = Instruction.Create(OpCodes.Ret);
    il.Append(Instruction.Create(OpCodes.Ldsfld, ids));
    il.Append(Instruction.Create(OpCodes.Brfalse, notFound));
    il.Append(Instruction.Create(OpCodes.Ldsfld, prices));
    il.Append(Instruction.Create(OpCodes.Brfalse, notFound));
    il.Append(Instruction.Create(OpCodes.Ldsfld, countField));
    il.Append(Instruction.Create(OpCodes.Stloc, count));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(loop);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldloc, count));
    il.Append(Instruction.Create(OpCodes.Bge, notFound));
    il.Append(Instruction.Create(OpCodes.Ldsfld, ids));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldelem_I4));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Bne_Un, next));
    if (hasType)
    {
        il.Append(Instruction.Create(OpCodes.Ldsfld, typeList!));
        il.Append(Instruction.Create(OpCodes.Ldloc, index));
        il.Append(Instruction.Create(OpCodes.Ldelem_I4));
        il.Append(Instruction.Create(OpCodes.Ldarg_1));
        il.Append(Instruction.Create(OpCodes.Bne_Un, next));
    }
    il.Append(Instruction.Create(OpCodes.Ldsfld, prices));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldelem_I4));
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(next);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(Instruction.Create(OpCodes.Br, loop));
    il.Append(notFound);
    il.Append(end);
    cache.Methods.Add(method);
}

static void StoreArrayValue(ILProcessor il, FieldDefinition array, VariableDefinition index,
    VariableDefinition value)
{
    il.Append(Instruction.Create(OpCodes.Ldsfld, array));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldloc, value));
    il.Append(Instruction.Create(OpCodes.Stelem_I4));
}

static void IncrementLocal(ILProcessor il, VariableDefinition local)
{
    il.Append(Instruction.Create(OpCodes.Ldloc, local));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Stloc, local));
}

static void IncrementField(ILProcessor il, FieldDefinition field)
{
    il.Append(Instruction.Create(OpCodes.Ldsfld, field));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Add));
    il.Append(Instruction.Create(OpCodes.Stsfld, field));
}

static void BuildFormatMethods(ModuleDefinition module, TypeDefinition cache)
{
    // Calling Int32.ToString directly with ``ldarg.0`` is invalid IL because
    // value-type instance calls require a managed address. Keep the call sites
    // simple and use a generated boxing helper that accepts the raw int32.
    var intToString = NewStatic(cache, "FormatInt", module.TypeSystem.String,
        ("value", module.TypeSystem.Int32));
    var intIL = intToString.Body.GetILProcessor();
    intIL.Append(Instruction.Create(OpCodes.Ldarg_0));
    intIL.Append(Instruction.Create(OpCodes.Box, module.TypeSystem.Int32));
    var objectToString = new MethodReference("ToString", module.TypeSystem.String,
        module.TypeSystem.Object) { HasThis = true };
    intIL.Append(Instruction.Create(OpCodes.Callvirt, objectToString));
    intIL.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(intToString);
    var concat = FindExistingMethod(module, "System.String", "Concat", 2) ??
        new MethodReference("Concat", module.TypeSystem.String, module.TypeSystem.String);
    if (concat.Parameters.Count == 0)
    {
        concat.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
        concat.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
    }
    var format = NewStatic(cache, "FormatMarketPrice", module.TypeSystem.String,
        ("price", module.TypeSystem.Int32), ("marketType", module.TypeSystem.Int32));
    var il = format.Body.GetILProcessor();
    var voucher = Instruction.Create(OpCodes.Ldstr, "代金券");
    var unitDone = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Ldstr, "\n  现价:    "));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Call, intToString));
    il.Append(Instruction.Create(OpCodes.Call, concat));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Beq, voucher));
    il.Append(Instruction.Create(OpCodes.Ldstr, "元宝"));
    il.Append(Instruction.Create(OpCodes.Br, unitDone));
    il.Append(voucher);
    il.Append(unitDone);
    il.Append(Instruction.Create(OpCodes.Call, concat));
    il.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(format);

    var coin = NewStatic(cache, "FormatCoinPrice", module.TypeSystem.String,
        ("price", module.TypeSystem.Int32));
    il = coin.Body.GetILProcessor();
    il.Append(Instruction.Create(OpCodes.Ldstr, "\n  现价:    "));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Call, intToString));
    il.Append(Instruction.Create(OpCodes.Call, concat));
    il.Append(Instruction.Create(OpCodes.Ldstr, "金币"));
    il.Append(Instruction.Create(OpCodes.Call, concat));
    il.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(coin);

    var multi = NewStatic(cache, "FormatMultiPrice", module.TypeSystem.String,
        ("price", module.TypeSystem.Int32), ("shopType", module.TypeSystem.Int32));
    il = multi.Body.GetILProcessor();
    var star = Instruction.Create(OpCodes.Ldstr, "星币");
    var honor = Instruction.Create(OpCodes.Ldstr, "荣誉");
    var arena = Instruction.Create(OpCodes.Ldstr, "竞技币");
    var family = Instruction.Create(OpCodes.Ldstr, "家族贡献");
    var unitEnd = Instruction.Create(OpCodes.Nop);
    var defaultUnit = Instruction.Create(OpCodes.Ldstr, "");
    il.Append(Instruction.Create(OpCodes.Ldstr, "\n  现价:    "));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Call, intToString));
    il.Append(Instruction.Create(OpCodes.Call, concat));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Switch, new[] { defaultUnit, star, honor, arena, family }));
    // ``switch`` falls through for values outside 0..4. That path must still
    // push a unit string before the final String.Concat call; branching
    // straight to ``unitEnd`` leaves only one argument on the evaluation
    // stack and Mono rejects the entire method as invalid IL.
    il.Append(Instruction.Create(OpCodes.Br, defaultUnit));
    il.Append(defaultUnit);
    il.Append(Instruction.Create(OpCodes.Br, unitEnd));
    il.Append(star);
    il.Append(Instruction.Create(OpCodes.Br, unitEnd));
    il.Append(honor);
    il.Append(Instruction.Create(OpCodes.Br, unitEnd));
    il.Append(arena);
    il.Append(Instruction.Create(OpCodes.Br, unitEnd));
    il.Append(family);
    il.Append(unitEnd);
    il.Append(Instruction.Create(OpCodes.Call, concat));
    il.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(multi);
}

static void BuildGetPriceText(ModuleDefinition module, TypeDefinition cache)
{
    var marketType = FindType(module, "Cal.DataTable.MarketBase");
    var shopType = FindType(module, "Cal.DataTable.ShopBase");
    var multiType = FindType(module, "Cal.DataTable.MultiShop");
    var marketItem = FindField(marketType, "ItemId");
    var shopItem = FindField(shopType, "ItemId");
    var multiItem = FindField(multiType, "ItemId");
    var multiShopType = FindField(multiType, "Type");
    var method = NewStatic(cache, "GetPriceText", module.TypeSystem.String,
        ("config", module.TypeSystem.Object), ("marketType", module.TypeSystem.Int32));
    var il = method.Body.GetILProcessor();
    var market = NewVariable(method, marketType);
    var shop = NewVariable(method, shopType);
    var multi = NewVariable(method, multiType);
    var marketItemId = NewVariable(method, module.TypeSystem.Int32);
    var price = NewVariable(method, module.TypeSystem.Int32);
    var checkShop = Instruction.Create(OpCodes.Nop);
    var checkMulti = Instruction.Create(OpCodes.Nop);
    var fallback = Instruction.Create(OpCodes.Ldnull);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Isinst, marketType));
    il.Append(Instruction.Create(OpCodes.Stloc, market));
    il.Append(Instruction.Create(OpCodes.Ldloc, market));
    il.Append(Instruction.Create(OpCodes.Brfalse, checkShop));
    il.Append(Instruction.Create(OpCodes.Ldloc, market));
    il.Append(Instruction.Create(OpCodes.Ldfld, marketItem));
    var yuanbaoPrice = Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "GetMarketPrice"));
    var voucherPrice = Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "GetVoucherPrice"));
    var useYuanbao = Instruction.Create(OpCodes.Nop);
    var marketPriceDone = Instruction.Create(OpCodes.Nop);
    il.Append(Instruction.Create(OpCodes.Stloc, marketItemId));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_1));
    il.Append(Instruction.Create(OpCodes.Bne_Un, useYuanbao));
    il.Append(Instruction.Create(OpCodes.Ldloc, marketItemId));
    il.Append(voucherPrice);
    il.Append(Instruction.Create(OpCodes.Stloc, price));
    il.Append(Instruction.Create(OpCodes.Br, marketPriceDone));
    il.Append(useYuanbao);
    il.Append(Instruction.Create(OpCodes.Ldloc, marketItemId));
    il.Append(yuanbaoPrice);
    il.Append(Instruction.Create(OpCodes.Stloc, price));
    il.Append(marketPriceDone);
    il.Append(Instruction.Create(OpCodes.Ldloc, price));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Blt, checkShop));
    il.Append(Instruction.Create(OpCodes.Ldloc, price));
    il.Append(Instruction.Create(OpCodes.Ldarg_1));
    il.Append(Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "FormatMarketPrice")));
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(checkShop);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Isinst, shopType));
    il.Append(Instruction.Create(OpCodes.Stloc, shop));
    il.Append(Instruction.Create(OpCodes.Ldloc, shop));
    il.Append(Instruction.Create(OpCodes.Brfalse, checkMulti));
    il.Append(Instruction.Create(OpCodes.Ldloc, shop));
    il.Append(Instruction.Create(OpCodes.Ldfld, shopItem));
    il.Append(Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "GetShopPrice")));
    il.Append(Instruction.Create(OpCodes.Stloc, price));
    il.Append(Instruction.Create(OpCodes.Ldloc, price));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Blt, checkMulti));
    il.Append(Instruction.Create(OpCodes.Ldloc, price));
    il.Append(Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "FormatCoinPrice")));
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(checkMulti);
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Isinst, multiType));
    il.Append(Instruction.Create(OpCodes.Stloc, multi));
    il.Append(Instruction.Create(OpCodes.Ldloc, multi));
    il.Append(Instruction.Create(OpCodes.Brfalse, fallback));
    il.Append(Instruction.Create(OpCodes.Ldloc, multi));
    il.Append(Instruction.Create(OpCodes.Ldfld, multiItem));
    il.Append(Instruction.Create(OpCodes.Ldloc, multi));
    il.Append(Instruction.Create(OpCodes.Ldfld, multiShopType));
    il.Append(Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "GetMultiPrice")));
    il.Append(Instruction.Create(OpCodes.Stloc, price));
    il.Append(Instruction.Create(OpCodes.Ldloc, price));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Blt, fallback));
    il.Append(Instruction.Create(OpCodes.Ldloc, price));
    il.Append(Instruction.Create(OpCodes.Ldloc, multi));
    il.Append(Instruction.Create(OpCodes.Ldfld, multiShopType));
    il.Append(Instruction.Create(OpCodes.Call, cache.Methods.Single(m => m.Name == "FormatMultiPrice")));
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(fallback);
    il.Append(Instruction.Create(OpCodes.Ret));
    cache.Methods.Add(method);
}

static void InjectPriceOverride(MethodDefinition method, MethodReference getPriceText, bool useMarketType = true)
{
    if (method.Body.Instructions.Any(i => i.Operand is MethodReference mr && mr.Name == "GetPriceText"))
        return;
    var original = method.Body.Instructions.First();
    var ret = Instruction.Create(OpCodes.Ret);
    var il = method.Body.GetILProcessor();
    il.InsertBefore(original, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(original, useMarketType
        ? Instruction.Create(OpCodes.Ldarg_2)
        : Instruction.Create(OpCodes.Ldc_I4_0));
    il.InsertBefore(original, Instruction.Create(OpCodes.Call, getPriceText));
    il.InsertBefore(original, Instruction.Create(OpCodes.Dup));
    il.InsertBefore(original, Instruction.Create(OpCodes.Brtrue, ret));
    il.InsertBefore(original, Instruction.Create(OpCodes.Pop));
    il.Append(ret);
}

static void InjectBagDescriptionApply(ModuleDefinition module, TypeReference cacheType)
{
    var candidates = new List<MethodDefinition>
    {
        FindType(module, "ET.UpdateBagUIEvent").Methods.Single(m => m.Name == "Run"),
    };
    var worn = FindType(module, "ET.UpdateWornEquipUIEvent");
    candidates.AddRange(worn.NestedTypes.SelectMany(t => t.Methods).Where(m => m.Name == "MoveNext"));
    foreach (var method in candidates)
        InjectDescriptionIntoMethod(module, cacheType, method);
}

static void InjectDescriptionIntoMethod(ModuleDefinition module, TypeReference cacheType,
    MethodDefinition method)
{
    if (method.Body == null || method.Body.Instructions.Any(i => i.Operand is MethodReference mr && mr.Name == "ApplyDescription"))
        return;
    var sourceStore = method.Body.Instructions.FirstOrDefault(i =>
        i.OpCode == OpCodes.Stfld && i.Operand is FieldReference f && f.Name == "getSource");
    if (sourceStore == null)
        return;
    var netItemLocal = method.Body.Instructions.Take(method.Body.Instructions.IndexOf(sourceStore))
        .Reverse().Select(i => LocalFromLoad(method, i)).FirstOrDefault(v => v != null);
    if (netItemLocal == null)
        return;
    var netItemType = ExternalType(module, "ET.NetItem");
    var description = new FieldReference("<Description>k__BackingField", module.TypeSystem.String, netItemType);
    var getItemId = new MethodReference("get_ItemId", module.TypeSystem.Int32, netItemType) { HasThis = true };
    var apply = ExternalMethod(module, cacheType, "ApplyDescription", module.TypeSystem.Void,
        module.TypeSystem.Int32, module.TypeSystem.String);
    var il = method.Body.GetILProcessor();
    var cursor = sourceStore;
    foreach (var instruction in new[] {
        Instruction.Create(OpCodes.Ldloc, netItemLocal),
        Instruction.Create(OpCodes.Callvirt, getItemId),
        Instruction.Create(OpCodes.Ldloc, netItemLocal),
        Instruction.Create(OpCodes.Ldfld, description),
        Instruction.Create(OpCodes.Call, apply),
    })
    {
        il.InsertAfter(cursor, instruction);
        cursor = instruction;
    }
}

static VariableDefinition? LocalFromLoad(MethodDefinition method, Instruction instruction)
{
    if (instruction.OpCode == OpCodes.Ldloc_0) return method.Body.Variables[0];
    if (instruction.OpCode == OpCodes.Ldloc_1) return method.Body.Variables[1];
    if (instruction.OpCode == OpCodes.Ldloc_2) return method.Body.Variables[2];
    if (instruction.OpCode == OpCodes.Ldloc_3) return method.Body.Variables[3];
    if (instruction.OpCode == OpCodes.Ldloc || instruction.OpCode == OpCodes.Ldloc_S)
        return instruction.Operand as VariableDefinition;
    return null;
}

static void InjectActiveInfoHandler(ModuleDefinition module, MethodDefinition loadCatalog)
{
    var state = AllTypes(module).Single(t => t.FullName.Contains("M2C_SendActiveInfoHandler/<Run>d__0"));
    var method = state.Methods.Single(m => m.Name == "MoveNext");
    if (method.Body.Instructions.Any(i => i.Operand is MethodReference mr && mr.Name == "LoadCatalog"))
        return;
    var message = FindField(state, "message");
    var active = FindType(module, "ET.M2C_SendActiveInfo");
    var data = FindField(active, "ShopPriceData");
    var descriptions = FindField(active, "ShopDescriptionData");
    var cache = FindType(module, "ET.ServerShopPriceCache");
    var applyDescriptions = cache.Methods.Single(m => m.Name == "ApplyCatalogDescriptions");
    // MoveNext is an async state machine and runs once before the await and
    // again after it resumes. Loading a sizeable catalog at the first
    // instruction races the player's unit initialization and repeats work on
    // every resume. Attach to the normal completion path, after the original
    // handler has finished applying ActiveInfo.
    //
    // Do not insert immediately before SetResult: the compiler emits
    // ``ldflda <>t__builder`` first, leaving a managed pointer on the IL
    // evaluation stack. Calling LoadCatalog while that pointer is live makes
    // Unity's Mono runtime vulnerable to a main-thread stall during the array
    // allocations. Insert before that builder load instead, while the stack
    // is empty, then let the original SetResult sequence run unchanged.
    var setResult = method.Body.Instructions.FirstOrDefault(i =>
        i.Operand is MethodReference mr && mr.Name == "SetResult");
    var builderLoad = setResult == null
        ? null
        : method.Body.Instructions.TakeWhile(i => i != setResult)
            .LastOrDefault(i => i.OpCode == OpCodes.Ldflda &&
                i.Operand is FieldReference f && f.Name == "<>t__builder");
    var first = builderLoad
        ?? setResult
        ?? method.Body.Instructions.Last(i => i.OpCode == OpCodes.Ret);
    // The builder load is normally preceded by ``ldarg.0``. Include that
    // object load in the insertion point so the catalog call starts with an
    // empty evaluation stack as well (not even the state-machine reference
    // is kept live across the allocations).
    if (builderLoad != null)
    {
        var builderIndex = method.Body.Instructions.IndexOf(builderLoad);
        if (builderIndex > 0 && method.Body.Instructions[builderIndex - 1].OpCode == OpCodes.Ldarg_0)
            first = method.Body.Instructions[builderIndex - 1];
    }
    var il = method.Body.GetILProcessor();
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, message));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, data));
    il.InsertBefore(first, Instruction.Create(OpCodes.Call, loadCatalog));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, message));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, data));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, message));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, descriptions));
    il.InsertBefore(first, Instruction.Create(OpCodes.Call, applyDescriptions));
}

static void InjectMarketShowItems(ModuleDefinition module, TypeReference cacheType)
{
    var method = AllTypes(module).SelectMany(t => t.Methods)
        .SingleOrDefault(m => m.Name == "<AwakeAsync>g__ShowItems|3");
    if (method == null || method.Body == null || method.Body.Instructions.Any(i =>
        i.Operand is MethodReference mr && mr.Name == "LoadMarket"))
        return;
    var ownerParent = method.DeclaringType.DeclaringType
        ?? throw new InvalidOperationException("ShowItems owner missing");
    var marketRet = method.DeclaringType.Fields.FirstOrDefault(f => f.Name == "marketRet")
        ?? ownerParent.Fields.FirstOrDefault(f => f.Name == "marketRet")
        ?? throw new InvalidOperationException("ShowItems marketRet field missing");
    var ids = AllMethods(module).SelectMany(m => m.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        .Select(i => i.Operand).OfType<FieldReference>()
        .FirstOrDefault(f => f.Name == "MarketIdList")
        ?? throw new InvalidOperationException("MarketIdList reference missing in MarketUI");
    var prices = new FieldReference("PriceList", ids.FieldType, ids.DeclaringType);
    var descriptions = new FieldReference("DescriptionList", StringListType(module, ids.FieldType), ids.DeclaringType);
    var load = ExternalMethod(module, cacheType, "LoadMarket", module.TypeSystem.Void,
        ids.FieldType, prices.FieldType);
    var apply = ExternalMethod(module, cacheType, "ApplyMarketDescriptions", module.TypeSystem.Void,
        ids.FieldType, descriptions.FieldType);
    var first = method.Body.Instructions.First();
    var il = method.Body.GetILProcessor();
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, marketRet));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, ids));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, marketRet));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, prices));
    il.InsertBefore(first, Instruction.Create(OpCodes.Call, load));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, marketRet));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, ids));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldarg_0));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, marketRet));
    il.InsertBefore(first, Instruction.Create(OpCodes.Ldfld, descriptions));
    il.InsertBefore(first, Instruction.Create(OpCodes.Call, apply));
}

static void InjectShopFilters(ModuleDefinition module, TypeReference cacheType)
{
    var shop = FindType(module, "ET.ShopUI");
    var show = shop.Methods.SingleOrDefault(m => m.Name == "ShowItems");
    if (show?.Body != null && !show.Body.Instructions.Any(i =>
        i.Operand is MethodReference mr && mr.Name == "FilterServerCatalog"))
    {
        var getShopBase = show.Body.Instructions.Select(i => i.Operand).OfType<MethodReference>()
            .FirstOrDefault(m => m.Name == "GetShopBase" &&
                m.ReturnType is GenericInstanceType)
            ?? throw new InvalidOperationException("ShopUI GetShopBase call missing");
        var listType = module.ImportReference(getShopBase.ReturnType);
        var item = show.Body.Instructions.Select(i => i.Operand).OfType<FieldReference>()
            .First(f => f.Name == "ItemId" && f.DeclaringType.FullName == "Cal.DataTable.ShopBase");
        var filter = BuildShopListFilter(module, shop, cacheType, listType, item);
        var getShopInstruction = show.Body.Instructions.First(i =>
            i.Operand is MethodReference mr && mr.Name == "GetShopBase");
        var il = show.Body.GetILProcessor();
        // Filter into a new list before the original foreach starts.  This
        // leaves the compiler-generated enumerator try/finally untouched and
        // preserves the static data table so a later hot reload can re-list an
        // item that was previously disabled.
        il.InsertAfter(getShopInstruction, Instruction.Create(OpCodes.Call, filter));
    }

    var multi = FindType(module, "ET.MultiShopUI");
    var refresh = multi.Methods.SingleOrDefault(m => m.Name == "Refresh");
    if (refresh?.Body == null || refresh.Body.Instructions.Any(i =>
        i.Operand is MethodReference mr && mr.Name == "IsMultiItemVisible"))
        return;
    var itemRef = refresh.Body.Instructions.Select(i => i.Operand).OfType<FieldReference>()
        .FirstOrDefault(f => f.Name == "ItemId" && f.DeclaringType.FullName == "Cal.DataTable.MultiShop")
        ?? throw new InvalidOperationException("MultiShop ItemId reference missing");
    var getItem = refresh.Body.Instructions.Select(i => i.Operand).OfType<MethodReference>()
        .FirstOrDefault(m => m.Name == "get_Item")
        ?? throw new InvalidOperationException("MultiShop list getter missing");
    var shopTypeField = FindField(multi, "shopType");
    var incrementStore = refresh.Body.Instructions.Last(i => i.OpCode == OpCodes.Stloc_1 ||
        (i.OpCode.Name.StartsWith("stloc") && i.Operand is VariableDefinition variable && variable.Index == 1));
    var skip = refresh.Body.Instructions[(refresh.Body.Instructions.IndexOf(incrementStore) - 3)];
    var firstDisplayCtor = refresh.Body.Instructions.First(i => i.OpCode == OpCodes.Newobj &&
        i.Operand is MethodReference mr && mr.DeclaringType.FullName.Contains("DisplayClass"));
    var getPriceMulti = ExternalMethod(module, cacheType, "IsMultiItemVisible", module.TypeSystem.Boolean,
        itemRef.FieldType, module.TypeSystem.Int32);
    var ilMulti = refresh.Body.GetILProcessor();
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Ldloc, refresh.Body.Variables[0]));
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Ldloc, refresh.Body.Variables[1]));
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Callvirt, getItem));
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Ldfld, itemRef));
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Ldarg_0));
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Ldfld, shopTypeField));
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Call, getPriceMulti));
    ilMulti.InsertBefore(firstDisplayCtor, Instruction.Create(OpCodes.Brfalse, skip));
}

static MethodDefinition BuildShopListFilter(ModuleDefinition module, TypeDefinition owner,
    TypeReference cacheType, TypeReference listType, FieldReference itemField)
{
    var existing = owner.Methods.FirstOrDefault(m => m.Name == "FilterServerCatalog");
    if (existing != null)
        return existing;
    if (listType is not GenericInstanceType genericList || genericList.GenericArguments.Count != 1)
        throw new InvalidOperationException("ShopBase list type missing");

    var rowType = module.ImportReference(genericList.GenericArguments[0]);
    var method = NewStatic(owner, "FilterServerCatalog", listType, ("items", listType));
    var result = NewVariable(method, listType);
    var row = NewVariable(method, rowType);
    var index = NewVariable(method, module.TypeSystem.Int32);
    var il = method.Body.GetILProcessor();
    var returnNull = Instruction.Create(OpCodes.Ldnull);
    var loop = Instruction.Create(OpCodes.Nop);
    var next = Instruction.Create(OpCodes.Nop);
    var done = Instruction.Create(OpCodes.Ldloc, result);

    var ctor = ListConstructor(module, listType);
    var count = ListMethod(listType, "get_Count", module.TypeSystem.Int32);
    var getItem = ListMethod(listType, "get_Item", rowType, module.TypeSystem.Int32);
    var add = ListMethod(listType, "Add", module.TypeSystem.Void, rowType);
    var isVisible = ExternalMethod(module, cacheType, "IsShopItemVisible",
        module.TypeSystem.Boolean, itemField.FieldType);

    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Brfalse, returnNull));
    il.Append(Instruction.Create(OpCodes.Newobj, ctor));
    il.Append(Instruction.Create(OpCodes.Stloc, result));
    il.Append(Instruction.Create(OpCodes.Ldc_I4_0));
    il.Append(Instruction.Create(OpCodes.Stloc, index));
    il.Append(loop);
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Callvirt, count));
    il.Append(Instruction.Create(OpCodes.Bge, done));
    il.Append(Instruction.Create(OpCodes.Ldarg_0));
    il.Append(Instruction.Create(OpCodes.Ldloc, index));
    il.Append(Instruction.Create(OpCodes.Callvirt, getItem));
    il.Append(Instruction.Create(OpCodes.Stloc, row));
    il.Append(Instruction.Create(OpCodes.Ldloc, row));
    il.Append(Instruction.Create(OpCodes.Ldfld, module.ImportReference(itemField)));
    il.Append(Instruction.Create(OpCodes.Call, isVisible));
    il.Append(Instruction.Create(OpCodes.Brfalse, next));
    il.Append(Instruction.Create(OpCodes.Ldloc, result));
    il.Append(Instruction.Create(OpCodes.Ldloc, row));
    il.Append(Instruction.Create(OpCodes.Callvirt, add));
    il.Append(next);
    IncrementLocal(il, index);
    il.Append(Instruction.Create(OpCodes.Br, loop));
    il.Append(done);
    il.Append(Instruction.Create(OpCodes.Ret));
    il.Append(returnNull);
    il.Append(Instruction.Create(OpCodes.Ret));
    owner.Methods.Add(method);
    return method;
}

static TypeReference ExternalCacheType(ModuleDefinition module)
{
    var scope = module.AssemblyReferences.FirstOrDefault(r => r.Name == "Unity.Hotfix")
        ?? throw new InvalidOperationException("Unity.Hotfix assembly reference missing");
    return new TypeReference("ET", "ServerShopPriceCache", module, scope);
}

static TypeReference ExternalType(ModuleDefinition module, string fullName)
{
    var split = fullName.LastIndexOf('.');
    var scope = module.AssemblyReferences.FirstOrDefault(r => r.Name == "Unity.Hotfix")
        ?? throw new InvalidOperationException("Unity.Hotfix assembly reference missing");
    return new TypeReference(fullName[..split], fullName[(split + 1)..], module, scope);
}

static MethodReference ExternalMethod(ModuleDefinition module, TypeReference declaring,
    string name, TypeReference returnType, params TypeReference[] parameters)
{
    var method = new MethodReference(name, returnType, declaring) { HasThis = false };
    foreach (var parameter in parameters)
        method.Parameters.Add(new ParameterDefinition(module.ImportReference(parameter)));
    return method;
}

static MethodDefinition NewStatic(TypeDefinition type, string name, TypeReference returnType,
    params (string Name, TypeReference Type)[] parameters)
{
    var method = new MethodDefinition(name, MethodAttributes.Public | MethodAttributes.Static, returnType);
    foreach (var parameter in parameters)
        method.Parameters.Add(new ParameterDefinition(parameter.Name, ParameterAttributes.None, parameter.Type));
    return method;
}

static VariableDefinition NewVariable(MethodDefinition method, TypeReference type)
{
    var variable = new VariableDefinition(type);
    method.Body.Variables.Add(variable);
    return variable;
}

static MethodReference ListMethod(TypeReference listInt, string name, TypeReference returnType,
    params TypeReference[] parameters)
{
    if (PatchContext.Module is { } module)
    {
        var candidates = AllMethods(module)
            .SelectMany(m => m.Body?.Instructions ?? Enumerable.Empty<Instruction>())
            .Select(i => i.Operand)
            .OfType<MethodReference>()
            .Where(m => m.Name == name && m.Parameters.Count == parameters.Length &&
                SameListDefinition(m.DeclaringType, listInt))
            .ToArray();
        var existing = candidates.FirstOrDefault(m => SameListType(m.DeclaringType, listInt) &&
            m.Parameters.Select((p, i) => SameType(p.ParameterType, parameters[i])).All(v => v));
        if (existing != null)
            return existing;
        // Some Unity assemblies only contain List<T>.Add references where the
        // argument is the declaring generic parameter (!0), not a concrete
        // List<int>.  Specialize that real reference to the cache's List<int>
        // instance instead of inventing a standalone signature.
        var template = candidates.FirstOrDefault();
        if (template != null)
            return SpecializeListMethod(template, listInt);
    }

    // Fallback is retained for unusual stripped assemblies.  Normal Unity
    // client DLLs always take the branch above; a warning makes a future
    // incompatible assembly immediately visible during patch generation.
    Console.Error.WriteLine($"warning: original List method reference not found: {listInt.FullName}::{name}");
    var method = new MethodReference(name, returnType, listInt) { HasThis = true };
    foreach (var parameter in parameters)
        method.Parameters.Add(new ParameterDefinition(parameter));
    return method;
}

static MethodReference ListConstructor(ModuleDefinition module, TypeReference listType)
{
    var existing = AllMethods(module).SelectMany(m => m.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        .Select(i => i.Operand).OfType<MethodReference>()
        .FirstOrDefault(m => m.Name == ".ctor" && m.Parameters.Count == 0 &&
            SameListType(m.DeclaringType, listType));
    if (existing != null)
        return existing;
    return new MethodReference(".ctor", module.TypeSystem.Void, listType)
    {
        HasThis = true,
        CallingConvention = MethodCallingConvention.Default,
    };
}

static bool SameListType(TypeReference left, TypeReference right)
{
    if (SameType(left, right))
        return true;
    if (left is GenericInstanceType lg && right is GenericInstanceType rg)
    {
        if (!SameType(lg.ElementType, rg.ElementType) ||
            lg.GenericArguments.Count != rg.GenericArguments.Count)
            return false;
        return lg.GenericArguments.Select((arg, i) =>
            SameType(arg, rg.GenericArguments[i])).All(v => v);
    }
    return false;
}

static bool SameListDefinition(TypeReference left, TypeReference right)
{
    if (SameListType(left, right))
        return true;
    if (left is GenericInstanceType lg && right is GenericInstanceType rg)
        return SameType(lg.ElementType, rg.ElementType) &&
            lg.GenericArguments.Count == rg.GenericArguments.Count;
    return false;
}

static MethodReference SpecializeListMethod(MethodReference template, TypeReference listInt)
{
    var method = new MethodReference(template.Name, template.ReturnType, listInt)
    {
        HasThis = template.HasThis,
        ExplicitThis = template.ExplicitThis,
        CallingConvention = template.CallingConvention
    };
    foreach (var parameter in template.Parameters)
        method.Parameters.Add(new ParameterDefinition(parameter.Name, parameter.Attributes,
            parameter.ParameterType));
    return method;
}

static bool SameType(TypeReference left, TypeReference right)
    => string.Equals(left.FullName, right.FullName, StringComparison.Ordinal);

static MethodReference? FindExistingMethod(ModuleDefinition module, string declaringType, string name, int parameterCount)
    => AllMethods(module).SelectMany(m => m.Body?.Instructions ?? Enumerable.Empty<Instruction>())
        .Select(i => i.Operand).OfType<MethodReference>()
        .FirstOrDefault(m => m.DeclaringType.FullName == declaringType && m.Name == name &&
                             m.Parameters.Count == parameterCount);

static TypeDefinition FindType(ModuleDefinition module, string fullName)
    => AllTypes(module).Single(t => t.FullName == fullName);

static FieldDefinition FindField(TypeDefinition type, string name)
    => type.Fields.Single(f => f.Name == name);

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

static IEnumerable<MethodDefinition> AllMethods(ModuleDefinition module)
    => AllTypes(module).SelectMany(type => type.Methods);

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
    public void Populate(ModuleDefinition module)
    {
        foreach (var reference in module.GetTypeReferences())
        {
            if (reference.Scope is not AssemblyNameReference assemblyName)
                continue;
            var assembly = Resolve(assemblyName).MainModule;
            if (assembly.GetType(reference.FullName) != null)
                continue;
            assembly.Types.Add(new TypeDefinition(reference.Namespace, reference.Name,
                TypeAttributes.Public | TypeAttributes.Class, null));
        }
    }
    public void Dispose()
    {
        foreach (var assembly in assemblies.Values)
            assembly.Dispose();
        assemblies.Clear();
    }
}

static class PatchContext
{
    public static ModuleDefinition? Module;
}
