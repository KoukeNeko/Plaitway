using Plaitway.V1;

namespace Plaitway.Client;

/// <summary>
/// The daemon's profiles as the watch reports them, in priority order. The daemon is the only source of
/// truth: commands are sent as requests and their results arrive through the watch, so the list never
/// guesses a state.
/// </summary>
public sealed class ProfileSet
{
    private List<Profile> _profiles = [];

    /// <summary>Last known profiles in priority order, highest first. Kept while the daemon is unavailable so that a UI does not empty out on a reconnect.</summary>
    public IReadOnlyList<Profile> Profiles => _profiles;

    /// <summary>Takes one event of the watch into account.</summary>
    public void Apply(ProfileWatchEvent watchEvent)
    {
        switch (watchEvent)
        {
            case ProfilesSnapshot snapshot:
                _profiles = InPriorityOrder(snapshot.Profiles);
                break;
            case ProfileChanged changed:
                _profiles = InPriorityOrder(Upsert(changed.Profile));
                break;
            case ProfileRemoved removed:
                _profiles = _profiles.Where(profile => profile.Id != removed.Id).ToList();
                break;
            default:
                break;
        }
    }

    /// <summary>The profile with <paramref name="id"/>, or null.</summary>
    public Profile? Find(string id) => _profiles.FirstOrDefault(profile => profile.Id == id);

    private List<Profile> Upsert(Profile changed)
    {
        var updated = new List<Profile>(_profiles);
        var index = updated.FindIndex(profile => profile.Id == changed.Id);
        if (index >= 0)
        {
            updated[index] = changed;
        }
        else
        {
            updated.Add(changed);
        }

        return updated;
    }

    /// <summary>
    /// Priority is the daemon's: lower wins. Events arrive one profile at a time, so profiles with equal
    /// priority (in the middle of a reorder) keep the position they have.
    /// </summary>
    private static List<Profile> InPriorityOrder(IEnumerable<Profile> profiles) =>
        [.. profiles.OrderBy(profile => profile.Settings?.Priority ?? 0)];
}
