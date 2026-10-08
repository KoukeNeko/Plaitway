using Grpc.Core;
using Plaitway.Client.Config;

namespace Plaitway.Client.Tests.Config;

/// <summary>The behaviour of macos/Tests/PlaitwayTests/Client/ConfigDiagnosticTests.swift, case for case.</summary>
public sealed class ConfigDiagnosticTests
{
    // The messages are the daemon's own: internal/ovpn and internal/wg (ParseError), internal/profile
    // (the checks every text goes through) and the fake backend.
    public static TheoryData<string, int, string> Rejections => new()
    {
        { "line 6: route: \"notamask\" is not a netmask", 6, "route: \"notamask\" is not a netmask" },
        { "line 3: ca refers to a file; put the content inline in <ca> ... </ca>", 3, "ca refers to a file; put the content inline in <ca> ... </ca>" },
        { "line 4: auth-user-pass refers to a file; use it without an argument", 4, "auth-user-pass refers to a file; use it without an argument" },
        { "line 9: <key> is never closed", 9, "<key> is never closed" },
        { "line 12: text after </key>", 12, "text after </key>" },
        { "line 2: carriage return without line feed", 2, "carriage return without line feed" },
        { "line 5: line is longer than 4096 bytes", 5, "line is longer than 4096 bytes" },
        { "line 2: PrivateKey is not a base64-encoded 32-byte key", 2, "PrivateKey is not a base64-encoded 32-byte key" },
        { "line 8: PostUp is not allowed: profiles cannot run commands", 8, "PostUp is not allowed: profiles cannot run commands" },
        { "line 3: unknown directive \"foo\"", 3, "unknown directive \"foo\"" },
        { "line 5: more than one [Interface] section", 5, "more than one [Interface] section" },
        { "line 1: [Interface] has no PrivateKey", 1, "[Interface] has no PrivateKey" },
        { "line 9: rejected by \"# fake: reject\"", 9, "rejected by \"# fake: reject\"" },
        { "line 120000: x", 120_000, "x" },
        { "line 2: first reason\nsecond line of it", 2, "first reason\nsecond line of it" },
    };

    [Theory]
    [MemberData(nameof(Rejections))]
    public void ReadsTheLineOfARejection(string message, int line, string reason) =>
        Assert.Equal(new ConfigDiagnostic(line, reason), ConfigDiagnostic.FromRejection(message));

    [Theory]
    [InlineData("profile has no remote server")]
    [InlineData("no [Interface] section")]
    [InlineData("no [Peer] section")]
    [InlineData("profile is empty")]
    [InlineData("profile is not valid UTF-8 text")]
    [InlineData("profile is larger than 1048576 bytes")]
    [InlineData("profile is 1048577 bytes, the limit is 1048576")]
    [InlineData("this profile is OpenVPN, but the text is WireGuard")]
    [InlineData("this profile is WireGuard, but the text is OpenVPN")]
    [InlineData("line is longer than it should be")]
    [InlineData("line 0: not a line")]
    [InlineData("line x: not a number")]
    [InlineData("line 3 is the problem")]
    [InlineData("line 99999999999999999999: too large for a line number")]
    [InlineData("Line 3: capital")]
    [InlineData("an error on line 3: in the middle")]
    [InlineData("")]
    public void ARejectionWithoutALineIsNotTiedToOne(string message) =>
        Assert.Equal(new ConfigDiagnostic(null, message), ConfigDiagnostic.FromRejection(message));

    [Fact]
    public void ReadsTheRejectionOfAnRpcError()
    {
        var error = new RpcException(new Status(StatusCode.InvalidArgument, "line 7: remote: bad"));
        Assert.Equal(new ConfigDiagnostic(7, "remote: bad"), ConfigDiagnostic.FromError(error));
    }

    public static TheoryData<StatusCode, string> NotRejections => new()
    {
        { StatusCode.PermissionDenied, "uid 501 is not an administrator, which /plaitway.v1.DaemonService/UpdateProfileContent requires" },
        { StatusCode.Unavailable, "the daemon is not running" },
        { StatusCode.NotFound, "line 3: not found" },
        { StatusCode.Internal, "line 3: internal" },
    };

    [Theory]
    [MemberData(nameof(NotRejections))]
    public void AnErrorThatIsNotARejectionOfTheTextIsNoDiagnostic(StatusCode code, string message) =>
        Assert.Null(ConfigDiagnostic.FromError(new RpcException(new Status(code, message))));

    [Fact]
    public void AForeignErrorIsNoDiagnostic() =>
        Assert.Null(ConfigDiagnostic.FromError(new InvalidOperationException("boom")));
}
