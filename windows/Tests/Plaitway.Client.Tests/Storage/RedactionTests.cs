using Plaitway.Client.Import;
using Plaitway.Client.Storage;

namespace Plaitway.Client.Tests.Storage;

/// <summary>A secret does not reach a log line or a debugger view through <see cref="object.ToString"/>.</summary>
public sealed class RedactionTests
{
    private const string Password = "hunter2-correct-horse";
    private const string PrivateKeyBody = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC-private";

    [Fact]
    public void CredentialsPrintTheUserNameAndNotThePassword()
    {
        var text = new Credentials("alice", Password).ToString();

        Assert.DoesNotContain(Password, text, StringComparison.Ordinal);
        Assert.Contains("alice", text, StringComparison.Ordinal);
        Assert.Equal("Credentials { Username = alice, Password = [hidden] }", text);
    }

    [Fact]
    public void AKeyPassphrasePrintsNoSecret()
    {
        var text = new Credentials(Password).ToString();

        Assert.DoesNotContain(Password, text, StringComparison.Ordinal);
    }

    [Fact]
    public void CredentialsStillCompareByValue()
    {
        Assert.Equal(new Credentials("alice", Password), new Credentials("alice", Password));
        Assert.NotEqual(new Credentials("alice", Password), new Credentials("alice", "other"));
    }

    [Fact]
    public void AnInlinedProfilePrintsNeitherItsTextNorItsPassword()
    {
        var inlined = new InlinedProfile($"<key>\n{PrivateKeyBody}\n</key>\n", new Credentials("alice", Password));

        var text = inlined.ToString();

        Assert.DoesNotContain(PrivateKeyBody, text, StringComparison.Ordinal);
        Assert.DoesNotContain(Password, text, StringComparison.Ordinal);
        Assert.Equal("InlinedProfile { Text = [hidden], Credentials = Credentials { Username = alice, Password = [hidden] } }", text);
    }


    [Fact]
    public void ALoadedProfilePrintsNeitherItsContentNorItsPassword()
    {
        var loaded = new LoadedProfile(System.Text.Encoding.UTF8.GetBytes(PrivateKeyBody), new Credentials("alice", Password));

        var text = loaded.ToString();

        Assert.DoesNotContain(PrivateKeyBody, text, StringComparison.Ordinal);
        Assert.DoesNotContain(Password, text, StringComparison.Ordinal);
    }
}
