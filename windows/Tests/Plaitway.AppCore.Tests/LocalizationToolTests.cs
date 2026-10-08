using System.Globalization;
using System.Xml.Linq;
using Plaitway.AppCore.Tests.Support;
using Plaitway.Client.Tests.Support;
using Plaitway.Localization;

namespace Plaitway.AppCore.Tests;

/// <summary>The tool that turns the string catalogs into .resw files and into <c>UiText</c>.</summary>
public sealed class LocalizationToolTests
{
    private static string Catalog(params (string Key, string? Chinese, string State)[] entries)
    {
        var strings = string.Join(
            ",\n",
            entries.Select(entry => entry.Chinese is null
                ? $"\"{entry.Key}\": {{}}"
                : $"\"{entry.Key}\": {{ \"localizations\": {{ \"zh-Hant\": {{ \"stringUnit\": {{ \"state\": \"{entry.State}\", \"value\": \"{entry.Chinese}\" }} }} }} }}"));
        return $"{{ \"sourceLanguage\": \"en\", \"strings\": {{ {strings} }}, \"version\": \"1.0\" }}";
    }

    private static StringSet Load(params string[] catalogs)
    {
        using var directory = TempDirectory.With(catalogs.Select((text, index) => (Name: $"{index}.xcstrings", Text: text)).ToDictionary(item => item.Name, item => item.Text));
        return StringSet.Load(catalogs.Select((_, index) => directory.Resolve($"{index}.xcstrings")));
    }

    [Theory]
    [InlineData("Connected", "Connected")]
    [InlineData("Connected: %lld", "Connected: {0}")]
    [InlineData("Cannot read %@: %@", "Cannot read {0}: {1}")]
    [InlineData("%2$@ before %1$@", "{1} before {0}")]
    [InlineData("100%% sure", "100% sure")]
    [InlineData("a {brace} stays", "a {{brace}} stays")]
    [InlineData("%@/s", "{0}/s")]
    public void FormatSpecifiersBecomeNetPlaceholders(string catalogText, string expected)
    {
        Assert.Equal(expected, FormatSpecifier.ToCompositeFormat(catalogText));
    }

    [Fact]
    public void ThePlaceholdersOfTheResultFormatToWhatTheCatalogMeant()
    {
        var format = FormatSpecifier.ToCompositeFormat("%2$@ before %1$@ (%3$lld)");

        Assert.Equal("b before a (3)", string.Format(CultureInfo.InvariantCulture, format, "a", "b", 3));
    }

    [Fact]
    public void ASpecifierKnowsWhatItFormats()
    {
        Assert.Equal([new FormatSpecifier(0, ArgumentKind.Text), new FormatSpecifier(1, ArgumentKind.Number)], FormatSpecifier.In("%@ and %lld"));
        Assert.Empty(FormatSpecifier.In("100%% sure"));
        Assert.Equal(FormatSpecifier.In("%1$@ %2$lld"), FormatSpecifier.In("%@ %lld"));
    }

    [Theory]
    [InlineData("Connected", "Connected")]
    [InlineData("Import Profile…", "ImportProfileEllipsis")]
    [InlineData("Quit Plaitway?", "QuitPlaitwayQuestion")]
    [InlineData("Quit Plaitway", "QuitPlaitway")]
    [InlineData("Connected: %lld", "ConnectedColon")]
    [InlineData("Delete “%@”?", "DeleteQuestion")]
    [InlineData("%@/s", "PerS")]
    [InlineData("Exclude private IPs", "ExcludePrivateIPs")]
    [InlineData("Wi-Fi", "WiFi")]
    public void AResourceIdIsMadeFromTheEnglishTextAlone(string key, string expected)
    {
        Assert.Equal(expected, ResourceId.From(key));
    }

    [Fact]
    public void TextsThatDifferOnlyInPunctuationGiveDifferentIds()
    {
        var keys = new[] { "Settings", "Settings…", "Quit Plaitway", "Quit Plaitway?", "Helper log", "Helper log: %@", "Delete", "Delete “%@”?" };

        Assert.Equal(keys.Length, keys.Select(ResourceId.From).Distinct().Count());
    }

    [Fact]
    public void TwoKeysWithOneIdAreReportedBeforeAnythingIsWritten()
    {
        var set = Load(Catalog(("Save File", "儲存", "translated"), ("Save file", "存檔", "translated")));

        Assert.Contains(set.Problems, problem => problem.Contains("SaveFile", StringComparison.Ordinal));
    }

    [Fact]
    public void AKeyInTwoCatalogsIsReported()
    {
        var set = Load(Catalog(("Save", "儲存", "translated")), Catalog(("Save", "存檔", "translated")));

        Assert.Contains(set.Problems, problem => problem.Contains("\"Save\" is in", StringComparison.Ordinal));
    }

    [Fact]
    public void AMissingEmptyOrUnfinishedTranslationIsReported()
    {
        var set = Load(Catalog(("Alpha", null, "translated"), ("Beta", " ", "translated"), ("Gamma", "丙", "needs_review"), ("Delta", "丁", "translated")));

        Assert.Equal(3, set.Problems.Count);
        Assert.Contains(set.Problems, problem => problem.Contains("Alpha", StringComparison.Ordinal) && problem.Contains("no zh-Hant", StringComparison.Ordinal));
        Assert.Contains(set.Problems, problem => problem.Contains("Beta", StringComparison.Ordinal) && problem.Contains("empty", StringComparison.Ordinal));
        Assert.Contains(set.Problems, problem => problem.Contains("Gamma", StringComparison.Ordinal) && problem.Contains("needs_review", StringComparison.Ordinal));
        Assert.Equal(["Delta"], set.Strings.Select(text => text.Key));
    }

    [Fact]
    public void ATranslationWithOtherPlaceholdersIsReported()
    {
        var set = Load(Catalog(("Connected: %lld", "已連線", "translated"), ("Rename %@ to %@", "重新命名 %@", "translated"), ("Fine %@", "沒問題 %@", "translated")));

        Assert.Equal(2, set.Problems.Count);
        Assert.All(set.Problems, problem => Assert.Contains("format specifiers differ", problem, StringComparison.Ordinal));
    }

    [Fact]
    public void ThePositionOfAPlaceholderMayChangeInATranslation()
    {
        var set = Load(Catalog(("Rename %@ to %@", "將 %2$@ 重新命名為 %1$@", "translated")));

        Assert.Empty(set.Problems);
        Assert.Equal("將 {1} 重新命名為 {0}", set.Strings[0].Formats["zh-TW"]);
    }

    [Fact]
    public void PluralVariationsAreRefusedBecauseAReswStringHasNoPluralForms()
    {
        const string catalog = """
            { "sourceLanguage": "en", "strings": { "%lld files": { "localizations": { "zh-Hant": { "variations": { "plural": { "other": { "stringUnit": { "state": "translated", "value": "%lld 個檔案" } } } } } } } }, "version": "1.0" }
            """;

        var error = Assert.Throws<CatalogException>(() => Load(catalog));

        Assert.Contains("variations", error.Message, StringComparison.Ordinal);
    }

    [Fact]
    public void ACatalogInAnotherSourceLanguageIsRefused()
    {
        Assert.Throws<CatalogException>(() => Load("{ \"sourceLanguage\": \"de\", \"strings\": {}, \"version\": \"1.0\" }"));
    }

    [Fact]
    public void AReswFileHoldsEveryStringOfALanguageAsTheCompositeFormat()
    {
        var set = Load(Catalog(("Connected", "已連線", "translated"), ("Connected: %lld", "已連線：%lld", "translated"), ("Tom & <Jerry>", "湯姆與傑利", "translated")));

        var resw = XDocument.Parse(ReswWriter.Render(set.Strings, Language.TraditionalChinese));

        var values = resw.Root!.Elements("data").ToDictionary(data => (string)data.Attribute("name")!, data => data.Element("value")!.Value);
        Assert.Equal("已連線", values["Connected"]);
        Assert.Equal("已連線：{0}", values["ConnectedColon"]);
        Assert.Equal("湯姆與傑利", values["TomJerry"]);
        Assert.Equal(["resmimetype", "version", "reader", "writer"], resw.Root.Elements("resheader").Select(header => (string)header.Attribute("name")!));
        Assert.All(resw.Root.Elements("data"), data => Assert.Equal("preserve", (string?)data.Attribute(XNamespace.Xml + "space")));

        var english = XDocument.Parse(ReswWriter.Render(set.Strings, Language.English));
        Assert.Equal("Tom & <Jerry>", english.Root!.Elements("data").Single(data => (string)data.Attribute("name")! == "TomJerry").Element("value")!.Value);
    }

    [Fact]
    public void TheClassTheCodeReadsStringsFromHasAMemberPerString()
    {
        var set = Load(Catalog(("Connected", "已連線", "translated"), ("Connected: %lld", "已連線：%lld", "translated"), ("Cannot read %@: %@", "無法讀取 %@：%@", "translated")));

        var source = UiTextWriter.Render(set.Strings, "Some.Namespace");

        Assert.Contains("namespace Some.Namespace;", source, StringComparison.Ordinal);
        Assert.Contains("public string Connected => localizer.Resolve(\"Connected\");", source, StringComparison.Ordinal);
        Assert.Contains("public string ConnectedColon(long number) => Format(\"ConnectedColon\", number);", source, StringComparison.Ordinal);
        Assert.Contains("public string CannotReadColon(string value1, string value2) => Format(\"CannotReadColon\", value1, value2);", source, StringComparison.Ordinal);
    }

    [Fact]
    public void TheShippedCatalogsAreFreeOfProblems()
    {
        Assert.Empty(TestText.Catalogs.Problems);
        Assert.NotEmpty(TestText.Catalogs.Strings);
    }
}
