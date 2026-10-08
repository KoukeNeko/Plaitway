using Plaitway.V1;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>Profiles as the daemon would report them, built by hand.</summary>
internal static class Profiles
{
    public static Profile Make(string id = "p", string name = "Profile", ProfileState state = ProfileState.Disconnected, bool desired = false, ProfileKind kind = ProfileKind.Unspecified) =>
        new() { Id = id, Name = name, State = state, DesiredEnabled = desired, Kind = kind };
}
