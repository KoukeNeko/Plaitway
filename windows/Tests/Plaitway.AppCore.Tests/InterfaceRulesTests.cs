using System.Text.RegularExpressions;
using Plaitway.AppCore.Tests.Support;

namespace Plaitway.AppCore.Tests;

/// <summary>
/// The rules of windows/docs/ui-architecture.md that can be read off the sources: theme and accessibility. What cannot be
/// read off them (that the names reach the screen reader, that the focus goes where it should) is looked at in the running
/// app, by <c>Plaitway.App.UiTests</c> and <c>scripts\capture-ui.ps1</c>.
/// </summary>
public sealed partial class InterfaceRulesTests
{
    [GeneratedRegex("""="#[0-9A-Fa-f]{3,8}"|(Foreground|Background|Fill|Stroke|BorderBrush|Color)="(Red|Green|Blue|Black|White|Gray|Grey|Orange|Yellow|Purple|Pink|Brown)\b""")]
    private static partial Regex LiteralColour();

    [GeneratedRegex(@"\{StaticResource (?<key>\w+(Brush|Color))\}")]
    private static partial Regex StaticBrushReference();

    [GeneratedRegex(@"x:Key=""(?<key>\w+)""")]
    private static partial Regex DefinedKey();

    [GeneratedRegex(@"<(?<tag>Button|ToggleButton|HyperlinkButton|RepeatButton)(?=[\s/>])(?<attributes>[^>]*?)(?<selfClosed>/?)>", RegexOptions.Singleline)]
    private static partial Regex ButtonStart();

    [GeneratedRegex(@"<(?<tag>TextBox|PasswordBox|ComboBox|ProgressRing|ListView|Pivot|Slider)(?=[\s/>])(?<attributes>[^>]*?)/?>", RegexOptions.Singleline)]
    private static partial Regex NamedControlStart();

    [Fact]
    public void NoColourIsWrittenInTheSources()
    {
        // A colour that is not a theme resource does not follow the theme, the high-contrast themes or the accent colour.
        var offending = SourceFiles.Read(".xaml")
            .SelectMany(file => LiteralColour().Matches(file.Text).Select(match => $"{file.Name}: {match.Value}"))
            .ToList();
        Assert.Empty(offending);

        var code = SourceFiles.Read(".cs")
            .Where(file => Regex.IsMatch(file.Text, @"\bColors\.|Color\.From|new SolidColorBrush"))
            .Select(file => file.Name)
            .ToList();
        Assert.Empty(code);
    }

    [Fact]
    public void SystemBrushesAreThemeResources()
    {
        // A StaticResource is looked up once, so a brush taken that way keeps the colour of the theme it was made in.
        var xaml = SourceFiles.Read(".xaml");
        var defined = xaml.SelectMany(file => DefinedKey().Matches(file.Text).Select(match => match.Groups["key"].Value)).ToHashSet();
        var offending = xaml
            .SelectMany(file => StaticBrushReference().Matches(file.Text).Select(match => (file.Name, Key: match.Groups["key"].Value)))
            .Where(reference => !defined.Contains(reference.Key))
            .Select(reference => $"{reference.Name}: {reference.Key}")
            .ToList();
        Assert.Empty(offending);
    }

    [Fact]
    public void AButtonWithoutTextHasAName()
    {
        // An icon button is read as "button" and nothing more without one.
        var offending = SourceFiles.Read(".xaml")
            .SelectMany(file => ButtonStart().Matches(file.Text).Select(match => (file.Name, Start: match.Value)))
            .Where(button => !button.Start.Contains("Content=", StringComparison.Ordinal) && !button.Start.Contains("AutomationProperties.Name=", StringComparison.Ordinal))
            .Select(button => $"{button.Name}: {button.Start}")
            .ToList();
        Assert.Empty(offending);
    }

    [Fact]
    public void AnInputOrAListHasANameOrAHeader()
    {
        var offending = SourceFiles.Read(".xaml")
            .SelectMany(file => NamedControlStart().Matches(file.Text).Select(match => (file.Name, Start: match.Value)))
            .Where(control => !control.Start.Contains("Header=", StringComparison.Ordinal) && !control.Start.Contains("AutomationProperties.Name=", StringComparison.Ordinal))
            .Select(control => $"{control.Name}: {control.Start}")
            .ToList();
        Assert.Empty(offending);
    }

    [Fact]
    public void NothingIsPlacedByAbsoluteCoordinates()
    {
        // A Canvas puts things at a left offset, which does not turn around in a right-to-left layout.
        var offending = SourceFiles.Read(".xaml")
            .Where(file => file.Text.Contains("<Canvas", StringComparison.Ordinal) || file.Text.Contains("Canvas.Left", StringComparison.Ordinal))
            .Select(file => file.Name)
            .ToList();
        Assert.Empty(offending);
    }
}
