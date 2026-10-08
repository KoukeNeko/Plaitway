using System.Text.RegularExpressions;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.Text;
using Plaitway.Client.Tests.Support;
using Plaitway.Localization;

namespace Plaitway.AppCore.Tests;

/// <summary>
/// macos/Tests/PlaitwayTests/App/LocalizationTests.swift. The catalogs are edited by hand, so these keep them in step
/// with the sources and with the owner's rules for Traditional Chinese and for UI text. What the Swift test finds by
/// scanning for <c>String(localized:)</c> the compiler checks here: every string the code uses is a member of the
/// generated <c>UiText</c>, and a member exists only for a string that is in both languages.
/// </summary>
public sealed partial class LocalizationTests
{
    private static IReadOnlyList<(string Name, string Text)> Sources(params string[] extensions) => SourceFiles.Read(extensions);

    /// <summary>The strings the sources read, by the member of <c>UiText</c> they read.</summary>
    private static Dictionary<string, string> UsedIds()
    {
        var known = UiText.Ids.ToHashSet();
        var used = new Dictionary<string, string>();
        foreach (var (name, text) in Sources(".cs", ".xaml"))
        {
            foreach (Match match in TextMember().Matches(text))
            {
                var member = match.Groups["member"].Value;
                if (known.Contains(member))
                {
                    used[member] = name;
                }
            }
        }

        return used;
    }

    // The instance of UiText is called text, Text, uiText or _text everywhere; a binding in XAML says ViewModel.Text.Something.
    [GeneratedRegex(@"\b_?(?:[tT]ext|uiText|UiText)\.(?<member>[A-Z][A-Za-z0-9]*)\b")]
    private static partial Regex TextMember();

    private static IEnumerable<(string Key, string Chinese)> ChineseOfEveryString() =>
        TestText.Catalogs.Strings.Select(text => (text.Key, text.Formats["zh-TW"]));

    // the catalogs

    [Fact]
    public void EveryStringIsTranslatedIntoTraditionalChineseWithTheSamePlaceholders()
    {
        // StringSet checks it for the build: a missing, empty or unfinished translation, other specifiers.
        Assert.Empty(TestText.Catalogs.Problems);

        foreach (var path in new[] { TestText.MacOsCatalogPath, TestText.WindowsCatalogPath })
        {
            foreach (var entry in CatalogReader.Read(path))
            {
                Assert.True(entry.Translations.TryGetValue("zh-Hant", out var unit), entry.Key);
                Assert.Equal("translated", unit.State);
                Assert.False(string.IsNullOrWhiteSpace(unit.Value), entry.Key);
                Assert.Equal(FormatSpecifier.In(entry.Key).Count, FormatSpecifier.In(unit.Value).Count);
            }
        }
    }

    [Fact]
    public void BothLanguagesHaveTheSameIdsAndNoEmptyString()
    {
        foreach (var text in TestText.Catalogs.Strings)
        {
            Assert.Equal(["en-US", "zh-TW"], text.Formats.Keys.Order());
            Assert.All(text.Formats.Values, format => Assert.False(string.IsNullOrWhiteSpace(format), text.Key));
        }
    }

    [Fact]
    public void TheCatalogsHaveNoStringInBoth()
    {
        // One source for each string: the Windows catalog is for what exists only on Windows.
        var mac = CatalogReader.Read(TestText.MacOsCatalogPath).Select(entry => entry.Key).ToHashSet();
        var windows = CatalogReader.Read(TestText.WindowsCatalogPath).Select(entry => entry.Key).ToList();

        Assert.All(windows, key => Assert.DoesNotContain(key, mac));
    }

    [Fact]
    public void TheTranslationsUseTaiwanTerms()
    {
        // Mainland terms and their Taiwan replacements: 匹配 -> 配對/符合, 當前 -> 目前, 組件 -> 元件, 緩存 -> 快取, ...
        string[] forbidden = ["匹配", "當前", "組件", "緩存", "軟件", "默認", "設置", "用戶", "網絡", "信息", "服務器", "登錄", "程序", "文件", "數據", "鏈接", "賬", "視頻", "支持"];

        foreach (var (key, chinese) in ChineseOfEveryString())
        {
            Assert.All(forbidden, term => Assert.DoesNotContain(term, chinese, StringComparison.Ordinal));
            Assert.True(chinese.Length > 0, key);
        }
    }

    [Fact]
    public void TheTextCarriesNoPersonality()
    {
        // Routine states, errors and dialogs: no exclamation marks, no "we" or "your", no replies as buttons.
        string[] bannedEnglish = [" we ", "we ", "your ", "!", "please", "oops", "sorry", "successfully"];
        string[] bannedChinese = ["我們", "你的", "您的", "！", "請稍候", "好的", "是的", "抱歉", "成功"];

        foreach (var text in TestText.Catalogs.Strings)
        {
            var english = text.Key.ToLowerInvariant();
            Assert.All(bannedEnglish, word => Assert.DoesNotContain(word, english, StringComparison.Ordinal));
            Assert.All(bannedChinese, word => Assert.DoesNotContain(word, text.Formats["zh-TW"], StringComparison.Ordinal));
        }
    }

    [Fact]
    public void LabelsAreNounsAndShortStatesAndNeverQuestionsExceptWhereTheUserDecides()
    {
        // A question belongs to a dialog that asks one; the keys that end in a question mark are exactly the dialogs.
        var questions = TestText.Catalogs.Strings.Where(text => text.Key.EndsWith('?')).Select(text => text.Key).Order();

        Assert.Equal(
            ["Delete “%@”?", "Quit Plaitway?", "Quit with unsaved changes?", "Reinstall helper?", "Remove route %@?", "Uninstall helper?"],
            questions);
    }

    // the generated code

    [Fact]
    public void TheGeneratedClassHasAMemberForEveryStringOfTheCatalogs()
    {
        Assert.Equal(TestText.Catalogs.Strings.Select(text => text.Id).Order(), UiText.Ids.Order());
        Assert.Equal(UiText.Ids.Count, UiText.Ids.Distinct().Count());
    }

    [Fact]
    public void EveryMemberResolvesInBothLanguagesAndFormatsItsArguments()
    {
        foreach (var tag in new[] { TestText.English, TestText.TraditionalChinese })
        {
            var text = TestText.For(tag);
            Assert.Equal(tag, text.Culture.Name);
            Assert.NotEmpty(text.Connected);
            Assert.NotEqual(text.Connected, TestText.For(tag == TestText.English ? TestText.TraditionalChinese : TestText.English).Connected);
        }

        Assert.Equal("Connected: 3", TestText.En.ConnectedColon(3));
        Assert.Equal("已連線：3", TestText.Zh.ConnectedColon(3));
        Assert.Equal("Shadowed by Office", TestText.En.ShadowedBy("Office"));
        Assert.Equal("被 Office 遮蔽", TestText.Zh.ShadowedBy("Office"));
        Assert.Equal("Delete “Office”?", TestText.En.DeleteQuestion("Office"));
    }

    [Fact]
    public void ThePlaceholdersOfEveryStringFormatInBothLanguages()
    {
        foreach (var text in TestText.Catalogs.Strings)
        {
            var arguments = text.Specifiers.Select(specifier => specifier.Kind == ArgumentKind.Text ? (object)"x" : 7L).ToArray();
            foreach (var format in text.Formats.Values)
            {
                var formatted = string.Format(System.Globalization.CultureInfo.InvariantCulture, format, arguments);
                Assert.DoesNotMatch(@"\{\d+\}", formatted);
            }
        }
    }

    // the sources

    [Fact]
    public void EveryStringTheWindowsCatalogHoldsIsUsed()
    {
        // The macOS catalog serves a macOS app that uses strings this one does not; the Windows catalog is only ours.
        var used = UsedIds();
        var unused = CatalogReader.Read(TestText.WindowsCatalogPath).Select(entry => ResourceId.From(entry.Key)).Where(id => !used.ContainsKey(id)).ToList();

        Assert.Empty(unused);
    }

    [Fact]
    public void TheScanOfTheSourcesFindsWhatItShould()
    {
        var used = UsedIds();

        Assert.True(used.Count > 100, $"the scan found too little: {used.Count}");
    }

    [Fact]
    public void XamlBindsOnlyToStringsThatExist()
    {
        var known = UiText.Ids.Concat(["Culture"]).ToHashSet();
        foreach (var (name, text) in Sources(".xaml"))
        {
            foreach (Match match in TextMember().Matches(text))
            {
                Assert.True(known.Contains(match.Groups["member"].Value), $"{name}: no string {match.Value}");
            }
        }
    }

    [Fact]
    public void NoUserVisibleStringIsWrittenInXaml()
    {
        // Text, labels and names all come from the resources; what XAML says literally is a binding.
        var properties = @"(?:Text|Content|Header|PlaceholderText|Title|Label|Description|PrimaryButtonText|SecondaryButtonText|CloseButtonText|AutomationProperties\.Name|AutomationProperties\.HelpText|ToolTipService\.ToolTip|OffContent|OnContent)";
        var attribute = new Regex(@"\s" + properties + "=\"(?<value>[^\"{][^\"]*)\"", RegexOptions.CultureInvariant);
        var element = new Regex(@">(?<value>[^<>{\s][^<>{]*)</(?:TextBlock|Run|Button|HyperlinkButton|ToggleSwitch|CheckBox|RadioButton|MenuFlyoutItem|ComboBoxItem|PivotItem)>", RegexOptions.CultureInvariant);

        foreach (var (name, text) in Sources(".xaml"))
        {
            var literals = attribute.Matches(text).Select(match => match.Value.Trim())
                .Concat(element.Matches(text).Select(match => match.Value))
                .Where(literal => !IsNotAWord(literal))
                .ToList();
            Assert.True(literals.Count == 0, $"{name}: literal text in XAML: {string.Join(" | ", literals)}");
        }
    }

    [Fact]
    public void NoSentenceIsWrittenInCSharp()
    {
        // A sentence, or a label with a colon or an ellipsis, belongs in the catalogs. A message for developers is not
        // for the user: an exception, a status of an RPC and a log line say what went wrong to the people who fix it, and
        // the format of a time stamp in the log file is not text either.
        var sentence = new Regex("\"(?<text>[^\"\\\\]*[A-Za-z]{2,}[ ][A-Za-z]{2,}[^\"\\\\]*)\"", RegexOptions.CultureInvariant);
        var developerFacing = new Regex(@"Exception\(|new Status\(|ArgumentException|Trace\.|Debug\.|\bAssert\b|LoggerMessage|\[SuppressMessage|Justification|\bnameof\b|Format = ");

        foreach (var (name, text) in Sources(".cs"))
        {
            // The log messages are for developers, and the diagnostics report is English whatever the language, as on macOS: the
            // people who read it in a bug report do not choose it.
            if (name.EndsWith("LogMessages.cs", StringComparison.Ordinal) || name.EndsWith("DiagnosticsReport.cs", StringComparison.Ordinal))
            {
                continue;
            }

            var offending = text.Split('\n')
                .Select((line, index) => (Line: line.Trim(), Number: index + 1))
                .Where(item => !item.Line.StartsWith("//", StringComparison.Ordinal) && !item.Line.StartsWith('*') && !developerFacing.IsMatch(item.Line))
                .SelectMany(item => sentence.Matches(item.Line).Select(match => $"{name}:{item.Number} {match.Groups["text"].Value}"))
                .ToList();
            Assert.True(offending.Count == 0, string.Join("\n", offending));
        }
    }

    [Fact]
    public void SameActionSameLabel()
    {
        // The action labels the owner's rule names: one wording everywhere an action appears.
        var used = UsedIds();
        foreach (var label in new[] { "ImportProfileEllipsis", "DeleteProfileEllipsis", "Connect", "Disconnect", "Retry", "ReinstallHelper", "Cancel", "Remove" })
        {
            Assert.True(used.ContainsKey(label), $"{label} is not used");
        }

        var keys = TestText.Catalogs.Strings.Select(text => text.Key).ToList();
        var importLabels = keys.Where(key => key.Contains("import", StringComparison.OrdinalIgnoreCase) && key.Contains('…', StringComparison.Ordinal)).ToList();
        Assert.Equal(["Import Profile…"], importLabels);
        var deleteLabels = keys.Where(key => key.StartsWith("Delete", StringComparison.Ordinal) && key.EndsWith('…')).ToList();
        Assert.Equal(["Delete Profile…"], deleteLabels);
    }

    [Fact]
    public void TheWordsOfTheMacAppAreTheWordsOfThisOne()
    {
        // The tab and the status words are reused, not reworded.
        Assert.Equal("已連線", TestText.Zh.Connected);
        Assert.Equal("路由與 DNS", TestText.Zh.RoutesAndDNS);
        Assert.Equal("組態", TestText.Zh.Configuration);
        Assert.Equal("被 Office 遮蔽", TestText.Zh.ShadowedBy("Office"));
        Assert.Equal("%@/秒".Replace("%@", "1 MB", StringComparison.Ordinal), TestText.Zh.PerS("1 MB"));
    }

    private static bool IsNotAWord(string literal) =>
        string.IsNullOrWhiteSpace(literal) || literal.All(character => !char.IsLetter(character));
}
