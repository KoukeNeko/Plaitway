using System.Globalization;
using System.Text.RegularExpressions;

namespace Plaitway.Client.Config;

/// <summary>
/// A problem the daemon found in profile text, for the editor to mark. The daemon rejects a text with
/// one reason, <c>line 12: route: "x" is not a netmask</c>, or without a line when the problem is the
/// text as a whole (<c>profile has no remote server</c>) or its kind.
/// </summary>
/// <param name="Line">
/// 1-based, counted in the text that was sent (with <see cref="SecretMask"/>, the restored text: see
/// <see cref="SecretMask.Restoration.DisplayLine"/>). Null when the problem is not tied to a line.
/// </param>
/// <param name="Message">The daemon's reason, English and without its <c>line N: </c> prefix.</param>
public sealed partial record ConfigDiagnostic(int? Line, string Message)
{
    /// <summary>Reads the message of a rejection.</summary>
    public static ConfigDiagnostic FromRejection(string rejection)
    {
        var match = LinePrefix().Match(rejection);
        if (match.Success
            && int.TryParse(match.Groups[1].Value, NumberStyles.None, CultureInfo.InvariantCulture, out var line)
            && line > 0)
        {
            return new ConfigDiagnostic(line, match.Groups[2].Value);
        }

        return new ConfigDiagnostic(null, rejection);
    }

    /// <summary>
    /// The diagnostic of an error from an update of the profile text or an import. Null when the daemon
    /// did not refuse the text: it is down, the caller is not an administrator, the profile is gone.
    /// </summary>
    public static ConfigDiagnostic? FromError(Exception error)
    {
        var failure = DaemonFailure.From(error);
        return failure.Kind == DaemonFailureKind.Rejected ? FromRejection(failure.Message) : null;
    }

    [GeneratedRegex(@"\Aline ([0-9]+): (.*)\z", RegexOptions.Singleline | RegexOptions.CultureInvariant)]
    private static partial Regex LinePrefix();
}
