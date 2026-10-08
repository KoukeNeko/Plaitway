using System.Security.Principal;
using Plaitway.Client.Transport;

namespace Plaitway.Client.Tests.Transport;

/// <summary>The owner rule, with the corpus of internal/transport/serverowner_windows_test.go (TestCheckServerOwner).</summary>
public sealed class PipeServerVerifierTests
{
    private const string SidCaller = "S-1-5-21-1111111111-2222222222-3333333333-1001";
    private const string SidOtherUser = "S-1-5-21-1111111111-2222222222-3333333333-1002";
    private const string SidBuiltinAdmin = "S-1-5-21-1111111111-2222222222-3333333333-500";
    private const string SidLocalService = "S-1-5-19";
    private const string SidNetworkService = "S-1-5-20";
    private const string SidServiceAccount = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464";
    private const string SidSystem = "S-1-5-18";
    private const string SidAdministrators = "S-1-5-32-544";
    private const string SidEveryone = "S-1-1-0";
    private const string SidUsers = "S-1-5-32-545";
    private const string SidAuthenticated = "S-1-5-11";
    private const string SidInteractive = "S-1-5-4";

    public static TheoryData<string, string, string, bool> Cases => new()
    {
        { "SYSTEM, the service", SidSystem, SidCaller, true },
        { "Administrators, an elevated daemon", SidAdministrators, SidCaller, true },
        { "the calling user, an unelevated daemon", SidCaller, SidCaller, true },
        { "SYSTEM when the caller is unknown", SidSystem, string.Empty, true },
        { "another user", SidOtherUser, SidCaller, false },
        { "the built-in Administrator account is not the Administrators group", SidBuiltinAdmin, SidCaller, false },
        { "Everyone", SidEveryone, SidCaller, false },
        { "Users", SidUsers, SidCaller, false },
        { "Authenticated Users", SidAuthenticated, SidCaller, false },
        { "Interactive", SidInteractive, SidCaller, false },
        { "Local Service", SidLocalService, SidCaller, false },
        { "Network Service", SidNetworkService, SidCaller, false },
        { "a per-service account", SidServiceAccount, SidCaller, false },
        { "another user when the caller is unknown", SidOtherUser, string.Empty, false },
        { "a caller that is SYSTEM trusts itself", SidSystem, SidSystem, true },
    };

    [Theory]
    [MemberData(nameof(Cases))]
    public void ChecksTheOwnerOfTheServer(string name, string owner, string caller, bool trusted)
    {
        var callerSid = caller.Length == 0 ? null : new SecurityIdentifier(caller);

        if (trusted)
        {
            PipeServerVerifier.CheckOwner(new SecurityIdentifier(owner), callerSid);
            return;
        }

        var error = Assert.Throws<PipeServerRefusedException>(() => PipeServerVerifier.CheckOwner(new SecurityIdentifier(owner), callerSid));
        Assert.StartsWith(PipeServerRefusedException.Prefix, error.Message, StringComparison.Ordinal);
        Assert.True(error.Message.Contains(owner, StringComparison.Ordinal), $"{name}: the error {error.Message} must name the owner {owner}");
    }

    [Fact]
    public void RefusesAMissingOwner()
    {
        var error = Assert.Throws<PipeServerRefusedException>(() => PipeServerVerifier.CheckOwner(null, new SecurityIdentifier(SidCaller)));
        Assert.StartsWith(PipeServerRefusedException.Prefix, error.Message, StringComparison.Ordinal);
    }

    [Fact]
    public void RefusalsStartWithTheSamePrefixAsTheGoClient() =>
        Assert.Equal("pipe server refused: ", PipeServerRefusedException.Prefix);

    [Fact]
    public void TheRefusalNamesTheOwnerAccountWhenItResolves()
    {
        var error = Assert.Throws<PipeServerRefusedException>(
            () => PipeServerVerifier.CheckOwner(new SecurityIdentifier(SidLocalService), new SecurityIdentifier(SidCaller)));
        // The account is named in the language of Windows; the SID is what stays unambiguous.
        Assert.Contains($"({SidLocalService})", error.Message, StringComparison.Ordinal);
        Assert.NotNull(error.Owner);
    }
}

/// <summary>The paths the Go client and daemon accept, with the cases of the Go tests (TestListenRejectsPathsThatAreNotPipeNames and checkSocketPath).</summary>
public sealed class PipePathTests
{
    [Theory]
    [InlineData(@"\\.\pipe\plaitway", "plaitway")]
    [InlineData(@"\\.\PIPE\plaitway", "plaitway")]
    [InlineData(@"\\.\pipe\plaitway-test-1-2", "plaitway-test-1-2")]
    public void AcceptsALocalNamedPipe(string path, string name)
    {
        var pipe = PipePath.Parse(path);
        Assert.Equal(name, pipe.Name);
        Assert.Equal(path, pipe.FullPath);
    }

    [Theory]
    [InlineData("")]
    [InlineData("plaitway")]
    [InlineData(@"C:\Users\someone\plaitway.sock")]
    [InlineData("/tmp/plaitway.sock")]
    [InlineData(@"\\.\pipe\")]
    [InlineData(@"\\.\pipe")]
    [InlineData(@"\\server\pipe\plaitway")]
    [InlineData(@"\\.\mailslot\plaitway")]
    public void RefusesWhatIsNotALocalNamedPipe(string path)
    {
        var error = Assert.Throws<InvalidPipePathException>(() => PipePath.Parse(path));
        Assert.Equal(path, error.Path);
        Assert.Contains(@"is not a named pipe, use \\.\pipe\<name>", error.Message, StringComparison.Ordinal);
        Assert.False(PipePath.TryParse(path, out _));
    }

    [Fact]
    public void TheDefaultIsTheDaemonsDefault() =>
        Assert.Equal(@"\\.\pipe\plaitway", PipePath.Default.FullPath);
}
