using System.Security.Principal;

namespace Plaitway.Client.Transport;

/// <summary>
/// A pipe was refused as the daemon's: its owner is none of the identities that may serve it, or the
/// check could not be made. The connection is closed before a byte is sent.
/// </summary>
public sealed class PipeServerRefusedException : IOException
{
    /// <summary>
    /// Starts the text of every error that refuses a pipe server, as <c>transport.RefusedServerPrefix</c>
    /// does in Go. A caller that gets the failure as a string can tell a refused server from a pipe that
    /// is missing by this.
    /// </summary>
    public const string Prefix = "pipe server refused: ";

    /// <summary>Refuses with <paramref name="reason"/> after <see cref="Prefix"/>.</summary>
    public PipeServerRefusedException(string reason, Exception? inner = null)
        : base(Prefix + reason, inner)
    {
    }

    /// <summary>The owner of the pipe, when it could be read.</summary>
    public SecurityIdentifier? Owner { get; init; }
}
