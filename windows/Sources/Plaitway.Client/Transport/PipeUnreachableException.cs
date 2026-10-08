namespace Plaitway.Client.Transport;

/// <summary>
/// The daemon's pipe could not be opened: it does not exist yet, all its instances stay busy, or this user
/// may not open it. Derived from <see cref="IOException"/> so that gRPC reports it as a call that could
/// not be started (<see cref="Grpc.Core.StatusCode.Unavailable"/>) and not as a failure of the client.
/// </summary>
public sealed class PipeUnreachableException : IOException
{
    /// <summary>Wraps what opening the pipe threw.</summary>
    /// <param name="pipe">The pipe that was opened.</param>
    /// <param name="accessDenied">True when Windows refused this user, rather than the pipe being missing or busy.</param>
    /// <param name="inner">What opening the pipe threw.</param>
    public PipeUnreachableException(PipePath pipe, bool accessDenied, Exception inner)
        : base($"cannot open {pipe}: {inner.Message}", inner)
    {
        AccessDenied = accessDenied;
    }

    /// <summary>True when Windows refused this user, rather than the pipe being missing or busy.</summary>
    public bool AccessDenied { get; }
}
