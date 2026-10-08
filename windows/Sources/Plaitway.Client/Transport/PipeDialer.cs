using System.IO.Pipes;
using System.Security.Principal;

namespace Plaitway.Client.Transport;

/// <summary>
/// Opens the daemon's pipe the way the Go client does (<c>transport.DialOptions</c>): with exactly the
/// access mask the pipe's access list grants interactive users and at identification level, and hands the
/// connection on only after <see cref="PipeServerVerifier"/> has accepted the server behind it.
/// </summary>
internal sealed class PipeDialer
{
    /// <summary>
    /// What a client may do with the pipe: read, write and wait on it, read its owner and access list
    /// (READ_CONTROL, which is how the server is verified), plus the attribute read that CreateFile adds
    /// to every open. It deliberately leaves out FILE_APPEND_DATA, which on a pipe is
    /// FILE_CREATE_PIPE_INSTANCE: GENERIC_WRITE maps to it, and the pipe's access list refuses a client
    /// that asks for it.
    /// </summary>
    public const PipeAccessRights ClientAccess =
        PipeAccessRights.ReadData
        | PipeAccessRights.WriteData
        | PipeAccessRights.ReadAttributes
        | PipeAccessRights.ReadPermissions
        | PipeAccessRights.Synchronize;

    private const string LocalServer = ".";

    /// <summary>How long to wait for a pipe that does not exist yet or whose instances are all busy.</summary>
    public static readonly TimeSpan DefaultConnectTimeout = TimeSpan.FromSeconds(1);

    private readonly Func<SecurityIdentifier> _lookupCaller;
    private readonly TimeSpan _connectTimeout;

    /// <param name="lookupCaller">
    /// The calling user. A seam so that a test can pose as another user, like <c>dialOptionsAs</c> in Go.
    /// A failure refuses the connection.
    /// </param>
    /// <param name="connectTimeout">How long to wait for the pipe; <see cref="DefaultConnectTimeout"/> when null.</param>
    public PipeDialer(Func<SecurityIdentifier>? lookupCaller = null, TimeSpan? connectTimeout = null)
    {
        _lookupCaller = lookupCaller ?? CurrentUser;
        _connectTimeout = connectTimeout ?? DefaultConnectTimeout;
    }

    /// <summary>Connects to the pipe and verifies its server.</summary>
    /// <exception cref="PipeServerRefusedException">The server is not trusted, or the check could not be made.</exception>
    public async ValueTask<Stream> ConnectAsync(PipePath path, CancellationToken cancellationToken)
    {
        var caller = LookupCaller();
        var pipe = new NamedPipeClientStream(
            LocalServer,
            path.Name,
            ClientAccess,
            PipeOptions.Asynchronous,
            TokenImpersonationLevel.Identification,
            HandleInheritability.None);
        try
        {
            await ConnectAsync(pipe, path, cancellationToken).ConfigureAwait(false);
            PipeServerVerifier.Verify(pipe, caller);
            return pipe;
        }
        catch
        {
            await pipe.DisposeAsync().ConfigureAwait(false);
            throw;
        }
    }

    private async Task ConnectAsync(NamedPipeClientStream pipe, PipePath path, CancellationToken cancellationToken)
    {
        try
        {
            await pipe.ConnectAsync((int)_connectTimeout.TotalMilliseconds, cancellationToken).ConfigureAwait(false);
        }
        catch (TimeoutException error)
        {
            throw new PipeUnreachableException(path, accessDenied: false, error);
        }
        catch (UnauthorizedAccessException error)
        {
            throw new PipeUnreachableException(path, accessDenied: true, error);
        }
    }

    private SecurityIdentifier LookupCaller()
    {
        try
        {
            return _lookupCaller();
        }
        catch (Exception error) when (error is not PipeServerRefusedException)
        {
            throw new PipeServerRefusedException($"cannot read the calling user: {error.Message}", error);
        }
    }

    /// <summary>The user the calling process runs as.</summary>
    private static SecurityIdentifier CurrentUser()
    {
        using var identity = WindowsIdentity.GetCurrent();
        return identity.User ?? throw new InvalidOperationException("the process token has no user");
    }
}
