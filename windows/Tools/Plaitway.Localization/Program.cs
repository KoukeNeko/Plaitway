using System.Text;

namespace Plaitway.Localization;

/// <summary>
/// Turns the string catalogs into what the app builds with:
/// <c>Plaitway.Localization resw --out DIR CATALOG...</c> writes <c>DIR/&lt;language&gt;/Resources.resw</c> for each language, and
/// <c>Plaitway.Localization uitext --out FILE --namespace NAME CATALOG...</c> writes the source of <c>UiText</c>.
/// Problems in the catalogs are printed as MSBuild errors and the exit code is 1.
/// </summary>
internal static class Program
{
    private const string ReswCommand = "resw";
    private const string UiTextCommand = "uitext";
    private const string OutOption = "--out";
    private const string NamespaceOption = "--namespace";
    private const string ReswFileName = "Resources.resw";
    private const int UsageExitCode = 2;
    private const int ProblemExitCode = 1;

    private static int Main(string[] arguments)
    {
        try
        {
            return Run(arguments);
        }
        catch (CatalogException error)
        {
            Console.Error.WriteLine($"{ToolName}: error PWL0002: {error.Message}");
            return ProblemExitCode;
        }
    }

    private static string ToolName => typeof(Program).Assembly.GetName().Name ?? "Plaitway.Localization";

    private static int Run(string[] arguments)
    {
        var options = Options.Parse(arguments);
        if (options is null)
        {
            Console.Error.WriteLine($"usage: {ToolName} ({ReswCommand}|{UiTextCommand}) {OutOption} PATH [{NamespaceOption} NAME] CATALOG...");
            return UsageExitCode;
        }

        var set = StringSet.Load(options.Catalogs);
        if (set.Problems.Count > 0)
        {
            foreach (var problem in set.Problems)
            {
                Console.Error.WriteLine($"{ToolName}: error PWL0001: {problem}");
            }

            return ProblemExitCode;
        }

        return options.Command == ReswCommand ? WriteResw(set, options.Out) : WriteUiText(set, options.Out, options.Namespace);
    }

    private static int WriteResw(StringSet set, string directory)
    {
        foreach (var language in Language.All)
        {
            var folder = Path.Combine(directory, language.Tag);
            Directory.CreateDirectory(folder);
            WriteIfChanged(Path.Combine(folder, ReswFileName), ReswWriter.Render(set.Strings, language));
        }

        return 0;
    }

    private static int WriteUiText(StringSet set, string file, string namespaceName)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(file))!);
        WriteIfChanged(file, UiTextWriter.Render(set.Strings, namespaceName));
        return 0;
    }

    /// <summary>Leaves a file alone when it says what would be written, so that the build that follows is not run again for nothing.</summary>
    private static void WriteIfChanged(string path, string content)
    {
        if (File.Exists(path) && File.ReadAllText(path) == content)
        {
            return;
        }

        File.WriteAllText(path, content, new UTF8Encoding(encoderShouldEmitUTF8Identifier: false));
    }

    private sealed record Options(string Command, string Out, string Namespace, IReadOnlyList<string> Catalogs)
    {
        public static Options? Parse(string[] arguments)
        {
            if (arguments.Length == 0 || arguments[0] is not (ReswCommand or UiTextCommand))
            {
                return null;
            }

            string? output = null;
            string? namespaceName = null;
            var catalogs = new List<string>();
            for (var index = 1; index < arguments.Length; index++)
            {
                switch (arguments[index])
                {
                    case OutOption when index + 1 < arguments.Length:
                        output = arguments[++index];
                        break;
                    case NamespaceOption when index + 1 < arguments.Length:
                        namespaceName = arguments[++index];
                        break;
                    default:
                        catalogs.Add(arguments[index]);
                        break;
                }
            }

            var needsNamespace = arguments[0] == UiTextCommand;
            if (output is null || catalogs.Count == 0 || (needsNamespace && namespaceName is null))
            {
                return null;
            }

            return new Options(arguments[0], output, namespaceName ?? string.Empty, catalogs);
        }
    }
}
