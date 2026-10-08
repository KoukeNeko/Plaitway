using Plaitway.AppCore.Editing;

namespace Plaitway.AppCore.Tests;

/// <summary>What the editor does with the line breaks of a text box, which counts every one as a single "\r".</summary>
public sealed class LineEndingsTests
{
    [Theory]
    [InlineData("a\r\nb\r\n", "a\nb\n")]
    [InlineData("a\rb\r", "a\nb\n")]
    [InlineData("a\nb\n", "a\nb\n")]
    [InlineData("a\r\n\r\nb\rc\n", "a\n\nb\nc\n")]
    [InlineData("", "")]
    public void NormalizesEveryKindOfBreakToALineFeed(string text, string expected) =>
        Assert.Equal(expected, LineEndings.Normalize(text));

    [Fact]
    public void RecognisesAFileMadeOnWindows()
    {
        Assert.True(LineEndings.UsesCarriageReturn("a\r\nb\r\n"));
        Assert.False(LineEndings.UsesCarriageReturn("a\nb\n"));
        Assert.False(LineEndings.UsesCarriageReturn("a\rb\r"));
    }

    [Fact]
    public void PutsTheFilesEndingBack()
    {
        Assert.Equal("a\r\nb\r\n", LineEndings.Restore("a\nb\n", usesCarriageReturn: true));
        Assert.Equal("a\nb\n", LineEndings.Restore("a\nb\n", usesCarriageReturn: false));
    }
}
