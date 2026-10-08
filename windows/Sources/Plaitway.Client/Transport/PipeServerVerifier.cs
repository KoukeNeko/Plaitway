using System.ComponentModel;
using System.IO.Pipes;
using System.Security.Principal;
using Plaitway.Client.Native;

namespace Plaitway.Client.Transport;

/// <summary>
/// Decides whether the server behind a connected pipe is the daemon, before anything is sent. The
/// clients send private keys and credentials, and any local user can create <c>\\.\pipe\plaitway</c>
/// while the service is not running.
/// </summary>
/// <remarks>
/// The owner is used because it is the one property of the server that an unprivileged client can read
/// and an unprivileged server cannot forge: asking for another owner when creating a pipe fails with
/// ERROR_INVALID_OWNER, so a pipe made by a standard user is always owned by that user. SYSTEM and
/// Administrators own what a service or an elevated daemon creates; the calling user owns what an
/// unelevated development daemon creates. The rule is the one of <c>internal/transport/serverowner_windows.go</c>.
/// </remarks>
internal static class PipeServerVerifier
{
    private const int NoError = 0;

    /// <summary>Reads the owner of the pipe instance behind <paramref name="pipe"/> and applies <see cref="CheckOwner"/>.</summary>
    /// <exception cref="PipeServerRefusedException">The owner is not trusted or could not be read.</exception>
    public static void Verify(NamedPipeClientStream pipe, SecurityIdentifier caller)
    {
        SecurityIdentifier owner;
        try
        {
            owner = ReadOwner(pipe);
        }
        catch (Win32Exception error)
        {
            throw new PipeServerRefusedException($"cannot read its owner: {error.Message}", error);
        }

        CheckOwner(owner, caller);
    }

    /// <summary>
    /// The rule itself. SYSTEM and Administrators are trusted, and so is the caller's own user. Anyone
    /// else, or an owner that is missing, is refused.
    /// </summary>
    /// <exception cref="PipeServerRefusedException">The owner is not trusted.</exception>
    public static void CheckOwner(SecurityIdentifier? owner, SecurityIdentifier? caller)
    {
        if (owner is null)
        {
            throw new PipeServerRefusedException("it has no valid owner");
        }

        if (owner.IsWellKnown(WellKnownSidType.LocalSystemSid)
            || owner.IsWellKnown(WellKnownSidType.BuiltinAdministratorsSid)
            || (caller is not null && owner.Equals(caller)))
        {
            return;
        }

        throw new PipeServerRefusedException(
            $"it is owned by {Describe(owner)}, not by SYSTEM, Administrators or {DescribeCaller(caller)}")
        {
            Owner = owner,
        };
    }

    /// <summary>
    /// The owner of the pipe instance behind a client handle, which needs READ_CONTROL on that handle
    /// (<see cref="PipeDialer.ClientAccess"/> has it).
    /// </summary>
    internal static SecurityIdentifier ReadOwner(NamedPipeClientStream pipe)
    {
        var status = SecurityNative.GetSecurityInfo(
            pipe.SafePipeHandle,
            SecurityNative.KernelObject,
            SecurityNative.OwnerSecurityInformation,
            out var owner,
            nint.Zero,
            nint.Zero,
            nint.Zero,
            out var descriptor);
        if (status != NoError)
        {
            throw new Win32Exception((int)status);
        }

        try
        {
            return owner == nint.Zero ? throw new Win32Exception("the security descriptor has no owner") : new SecurityIdentifier(owner);
        }
        finally
        {
            SecurityNative.LocalFree(descriptor);
        }
    }

    private static string DescribeCaller(SecurityIdentifier? caller) => caller is null ? "the calling user" : Describe(caller);

    /// <summary>Names the account when Windows can resolve it, always with the SID, which is the part that stays unambiguous.</summary>
    internal static string Describe(SecurityIdentifier sid)
    {
        try
        {
            return $"{sid.Translate(typeof(NTAccount)).Value} ({sid.Value})";
        }
        catch (IdentityNotMappedException)
        {
            return sid.Value;
        }
        catch (SystemException)
        {
            return sid.Value;
        }
    }
}
