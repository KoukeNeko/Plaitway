using Grpc.Core;
using Plaitway.Client.Transport;

namespace Plaitway.Client;

/// <summary>Why a call to the daemon failed, in the terms a UI words differently.</summary>
public enum DaemonFailureKind
{
    /// <summary>The caller is not an administrator, or not the console user.</summary>
    PermissionDenied,

    /// <summary>The daemon is not running or not reachable.</summary>
    Unavailable,

    /// <summary>A pipe answered, but its owner is not one that may serve the daemon's pipe. Nothing was sent.</summary>
    ServerRefused,

    /// <summary>The profile, or whatever the call named, does not exist.</summary>
    NotFound,

    /// <summary>The daemon refused the input; <see cref="DaemonFailure.Message"/> is its reason.</summary>
    Rejected,

    /// <summary>Anything else.</summary>
    Other,
}

/// <summary>
/// Why a call to the daemon failed: the kind, for the UI to word, and the message. The message is the
/// daemon's English text and is meant to be shown only where the cause is the input, such as a rejected
/// profile (<see cref="DaemonFailureKind.Rejected"/>).
/// </summary>
/// <param name="Kind">What failed.</param>
/// <param name="Message">The daemon's reason, or the text of the exception for failures that are not the daemon's.</param>
public readonly record struct DaemonFailure(DaemonFailureKind Kind, string Message)
{
    /// <summary>Classifies the exception of a call to the daemon.</summary>
    public static DaemonFailure From(Exception error) => error switch
    {
        PipeServerRefusedException refused => new DaemonFailure(DaemonFailureKind.ServerRefused, refused.Message),
        RpcException rpc => FromRpc(rpc),
        _ => new DaemonFailure(DaemonFailureKind.Other, error.Message),
    };

    private static DaemonFailure FromRpc(RpcException rpc)
    {
        var detail = rpc.Status.Detail;
        return rpc.StatusCode switch
        {
            StatusCode.PermissionDenied => new DaemonFailure(DaemonFailureKind.PermissionDenied, detail),
            StatusCode.Unavailable => FromUnavailable(rpc),
            StatusCode.NotFound => new DaemonFailure(DaemonFailureKind.NotFound, detail),
            StatusCode.InvalidArgument or StatusCode.FailedPrecondition => new DaemonFailure(DaemonFailureKind.Rejected, detail),
            _ => new DaemonFailure(DaemonFailureKind.Other, detail),
        };
    }

    /// <summary>gRPC wraps what a connect callback throws; why the pipe was not used is somewhere down the chain.</summary>
    private static DaemonFailure FromUnavailable(RpcException rpc)
    {
        for (var current = rpc.Status.DebugException ?? rpc.InnerException; current is not null; current = current.InnerException)
        {
            switch (current)
            {
                case PipeServerRefusedException refused:
                    return new DaemonFailure(DaemonFailureKind.ServerRefused, refused.Message);
                case PipeUnreachableException { AccessDenied: true } denied:
                    return new DaemonFailure(DaemonFailureKind.PermissionDenied, denied.Message);
                default:
                    break;
            }
        }

        return new DaemonFailure(DaemonFailureKind.Unavailable, rpc.Status.Detail);
    }
}