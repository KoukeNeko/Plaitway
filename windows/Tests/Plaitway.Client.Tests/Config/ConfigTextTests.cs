using Plaitway.Client.Config;

namespace Plaitway.Client.Tests.Config;

public sealed class ConfigTextTests
{
    [Theory]
    [InlineData("PrivateKey", "privatekey")]
    [InlineData("Pr\u0130vateKey", "privatekey")]
    [InlineData("Private\u212Aey", "privatekey")]
    [InlineData("\u0130", "i")]
    [InlineData("\u0131", "\u0131")]
    [InlineData("\u017F", "\u017F")]
    [InlineData("備註", "備註")]
    public void LowerCasesAsTheDaemonDoes(string text, string expected) =>
        Assert.Equal(expected, ConfigText.ToLowerAsDaemon(text));

    [Fact]
    public void ABlockTagWrittenWithTheKelvinSignClosesTheBlock() =>
        Assert.True(ConfigText.EqualsLowercased("</\u212Aey>", "</key>"));
}

public sealed class ValueShapesTests
{
    [Theory]
    [InlineData("::ffff:1.2.3.4")]
    [InlineData("64:ff9b::192.0.2.33")]
    [InlineData("::1")]
    [InlineData("fe80::1")]
    [InlineData("2001:db8::")]
    [InlineData("10.0.0.1")]
    public void AcceptsAddresses(string text) => Assert.True(ValueShapes.IsIPAddress(text));

    [Theory]
    [InlineData("::ffff:1.2.3.04")]
    [InlineData("::ffff:01.2.3.4")]
    [InlineData("::ffff:1.2.3")]
    [InlineData("::ffff:1.2.3.256")]
    [InlineData("::ffff:1.2.3.4.5")]
    [InlineData("::1.2.3.4x")]
    [InlineData("010.0.0.1")]
    [InlineData("[::1]")]
    [InlineData("fe80::1%eth0")]
    [InlineData("1")]
    public void RefusesWhatInetPtonRefuses(string text) => Assert.False(ValueShapes.IsIPAddress(text));
}
