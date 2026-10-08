using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

/// <summary>
/// What the notification-area icon says about all profiles together. What needs the person comes before what is
/// under way: a profile waiting for a password does not get there by itself.
/// </summary>
public enum AggregateState
{
    /// <summary>The helper does not answer: nothing the profiles say is true.</summary>
    Unavailable,

    /// <summary>Nothing is connected.</summary>
    Idle,

    /// <summary>At least one profile is connected and none needs anything.</summary>
    Connected,

    /// <summary>Something is under way.</summary>
    Connecting,

    /// <summary>A profile waits for a password.</summary>
    NeedsCredentials,

    /// <summary>A profile failed.</summary>
    Problem,
}

/// <summary>How the profiles add up, and how that is shown.</summary>
public static class AggregateStates
{
    /// <summary>The state of all <paramref name="profiles"/> given where the helper stands.</summary>
    public static AggregateState Of(DaemonSetup setup, IEnumerable<Profile> profiles)
    {
        if (setup.Kind == SetupKind.Connecting)
        {
            return AggregateState.Connecting;
        }

        return setup.IsUsable ? OfProfiles(profiles.Select(profile => profile.State).ToHashSet()) : AggregateState.Unavailable;
    }

    /// <summary>The shape of the state, which is the shield of the profile in the same state.</summary>
    public static StatusIcon Icon(this AggregateState state) => state switch
    {
        AggregateState.Unavailable => StatusIcon.Unavailable,
        AggregateState.Connected => StatusIcon.Connected,
        AggregateState.Connecting => StatusIcon.Connecting,
        AggregateState.NeedsCredentials => StatusIcon.AwaitingCredentials,
        AggregateState.Problem => StatusIcon.Failed,
        _ => StatusIcon.Idle,
    };

    /// <summary>The state in a word, for the icon's tooltip and its accessible name.</summary>
    public static string Label(this AggregateState state, UiText text) => state switch
    {
        AggregateState.Unavailable => text.HelperUnavailable,
        AggregateState.Idle => text.NotConnected,
        AggregateState.Connected => text.Connected,
        AggregateState.Connecting => text.Connecting,
        AggregateState.NeedsCredentials => text.AwaitingCredentials,
        _ => text.Problem,
    };

    private static AggregateState OfProfiles(HashSet<ProfileState> states)
    {
        if (states.Contains(ProfileState.Failed))
        {
            return AggregateState.Problem;
        }

        if (states.Contains(ProfileState.AwaitingCredentials))
        {
            return AggregateState.NeedsCredentials;
        }

        if (states.Overlaps([ProfileState.Connecting, ProfileState.Reconnecting, ProfileState.Disconnecting]))
        {
            return AggregateState.Connecting;
        }

        return states.Contains(ProfileState.Connected) ? AggregateState.Connected : AggregateState.Idle;
    }
}
