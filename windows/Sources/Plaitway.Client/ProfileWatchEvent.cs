using Plaitway.V1;

namespace Plaitway.Client;

/// <summary>One thing the profile watch reports.</summary>
public abstract record ProfileWatchEvent
{
    private protected ProfileWatchEvent()
    {
    }
}

/// <summary>Every profile, in the daemon's order. The first event of every connection to the daemon.</summary>
/// <param name="Profiles">The profiles the daemon holds.</param>
public sealed record ProfilesSnapshot(IReadOnlyList<Profile> Profiles) : ProfileWatchEvent;

/// <summary>A profile was added or changed.</summary>
/// <param name="Profile">The profile as it is now.</param>
public sealed record ProfileChanged(Profile Profile) : ProfileWatchEvent;

/// <summary>A profile was deleted.</summary>
/// <param name="Id">The id of the profile that is gone.</param>
public sealed record ProfileRemoved(string Id) : ProfileWatchEvent;

/// <summary>
/// The watch lost the daemon, or could not reach it. Reported once per outage; the watch keeps trying
/// and a <see cref="ProfilesSnapshot"/> follows when the daemon is back.
/// </summary>
/// <param name="Cause">What went wrong, for the log.</param>
public sealed record DaemonUnavailable(Exception Cause) : ProfileWatchEvent;
