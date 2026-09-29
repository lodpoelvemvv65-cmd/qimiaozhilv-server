using Mono.Cecil;
using Mono.Cecil.Cil;

if (args.Length != 2)
{
    Console.Error.WriteLine("usage: ClientTitlePatcher <input-dll> <output-dll>");
    return 2;
}

var inputPath = Path.GetFullPath(args[0]);
var outputPath = Path.GetFullPath(args[1]);
using var module = ModuleDefinition.ReadModule(inputPath, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
});

var numHelper = module.Types.Single(type => type.FullName == "ET.NumHelper");
var fillNum = numHelper.Methods.Single(method => method.Name == "FillNum");
if (!HasTitleAssignment(fillNum))
{
    var getNumeric = fillNum.Body.Instructions
        .Select(instruction => instruction.Operand)
        .OfType<GenericInstanceMethod>()
        .First(method => method.Name == "GetComponent"
            && method.GenericArguments.Any(argument => argument.FullName == "ET.NumericComponent"));
    var numericSet = fillNum.Body.Instructions
        .Select(instruction => instruction.Operand)
        .OfType<MethodReference>()
        .First(method => method.Name == "Set"
            && method.DeclaringType.FullName == "ET.NumericComponent");
    var unitCharacter = module.Types.Single(type => type.FullName == "ET.UnitCharacter");
    var getTitle = unitCharacter.Methods.Single(method => method.Name == "get_Title");
    var ret = fillNum.Body.Instructions.Last(instruction => instruction.OpCode == OpCodes.Ret);
    var il = fillNum.Body.GetILProcessor();

    foreach (var instruction in new[]
    {
        Instruction.Create(OpCodes.Ldarg_0),
        Instruction.Create(OpCodes.Callvirt, module.ImportReference(getNumeric)),
        Instruction.Create(OpCodes.Ldc_I4, 1038),
        Instruction.Create(OpCodes.Ldarg_1),
        Instruction.Create(OpCodes.Callvirt, module.ImportReference(getTitle)),
        Instruction.Create(OpCodes.Conv_R4),
        Instruction.Create(OpCodes.Ldc_I4_1),
        Instruction.Create(OpCodes.Callvirt, module.ImportReference(numericSet)),
    })
    {
        il.InsertBefore(ret, instruction);
    }
}

Directory.CreateDirectory(Path.GetDirectoryName(outputPath)!);
module.Write(outputPath);

using var verification = ModuleDefinition.ReadModule(outputPath, new ReaderParameters
{
    InMemory = true,
    ReadSymbols = false,
});
var verifiedFillNum = verification.Types.Single(type => type.FullName == "ET.NumHelper")
    .Methods.Single(method => method.Name == "FillNum");
if (!HasTitleAssignment(verifiedFillNum))
    throw new InvalidOperationException("NumHelper.FillNum title assignment was not written");

Console.WriteLine("verified NumHelper.FillNum: UnitCharacter.Title -> NumericType 1038");
return 0;

static bool HasTitleAssignment(MethodDefinition method)
{
    var instructions = method.Body.Instructions;
    for (var index = 0; index < instructions.Count; index++)
    {
        if (!LoadsInt32(instructions[index], 1038))
            continue;
        var window = instructions.Skip(index + 1).Take(7).ToList();
        if (window.Any(instruction => instruction.Operand is MethodReference called
                && called.Name == "get_Title"
                && called.DeclaringType.FullName == "ET.UnitCharacter")
            && window.Any(instruction => instruction.Operand is MethodReference called
                && called.Name == "Set"
                && called.DeclaringType.FullName == "ET.NumericComponent"))
        {
            return true;
        }
    }
    return false;
}

static bool LoadsInt32(Instruction instruction, int expected)
{
    return instruction.OpCode.Code switch
    {
        Code.Ldc_I4 => Convert.ToInt32(instruction.Operand) == expected,
        Code.Ldc_I4_S => Convert.ToInt32(instruction.Operand) == expected,
        _ => false,
    };
}
