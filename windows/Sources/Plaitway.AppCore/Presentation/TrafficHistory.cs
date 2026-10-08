using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

/// <summary>One reading of how fast data moves, in bytes per second.</summary>
/// <param name="Id">Unique among the samples of the history.</param>
/// <param name="Time">When the reading was taken.</param>
/// <param name="Received">Bytes per second towards this computer.</param>
/// <param name="Sent">Bytes per second away from it.</param>
public sealed record TrafficSample(int Id, DateTimeOffset Time, double Received, double Sent);

/// <summary>The current rates of a profile.</summary>
/// <param name="Received">Bytes per second received.</param>
/// <param name="Sent">Bytes per second sent.</param>
public readonly record struct TrafficRate(double Received, double Sent);

/// <summary>
/// How fast data moves through each connected profile, worked out here from the byte counters the daemon reports
/// every couple of seconds; the daemon keeps no history.
/// </summary>
public sealed class TrafficHistory
{
    /// <summary>Two minutes at the daemon's pace.</summary>
    public const int Capacity = 60;

    /// <summary>A reading closer than this to the last one is a second event for the same counters.</summary>
    private static readonly TimeSpan MinimumInterval = TimeSpan.FromSeconds(0.9);

    /// <summary>The rate shown is the mean of the last few samples; one reading jumps too much to read.</summary>
    private const int Smoothing = 3;

    private readonly Dictionary<string, List<TrafficSample>> _samples = [];
    private readonly Dictionary<string, Counters> _counters = [];
    private int _nextId;

    private readonly record struct Counters(DateTimeOffset Time, ulong Received, ulong Sent);

    /// <summary>The samples of a profile, oldest first; empty until there are two readings.</summary>
    public IReadOnlyList<TrafficSample> SamplesOf(string profileId) => _samples.TryGetValue(profileId, out var samples) ? samples : [];

    /// <summary>The samples of every profile that has some.</summary>
    public IReadOnlyDictionary<string, List<TrafficSample>> Samples => _samples;

    /// <summary>Takes the counters of <paramref name="profiles"/> at <paramref name="now"/>.</summary>
    public void Record(IEnumerable<Profile> profiles, DateTimeOffset now)
    {
        var connected = profiles.Where(profile => profile.State == ProfileState.Connected).ToList();
        ForgetProfilesThatAreNotConnected(connected);
        foreach (var profile in connected)
        {
            RecordProfile(profile, now);
        }
    }

    /// <summary>The current rates; null until there are two readings.</summary>
    public TrafficRate? RateOf(string profileId)
    {
        if (!_samples.TryGetValue(profileId, out var samples) || samples.Count == 0)
        {
            return null;
        }

        var recent = samples.TakeLast(Smoothing).ToList();
        return new TrafficRate(recent.Average(sample => sample.Received), recent.Average(sample => sample.Sent));
    }

    /// <summary>A reconnection starts the counters again, and so the graph.</summary>
    private void ForgetProfilesThatAreNotConnected(List<Profile> connected)
    {
        var ids = connected.Select(profile => profile.Id).ToHashSet();
        foreach (var id in _counters.Keys.Where(id => !ids.Contains(id)).ToList())
        {
            _counters.Remove(id);
            _samples.Remove(id);
        }
    }

    private void RecordProfile(Profile profile, DateTimeOffset now)
    {
        var received = profile.Status?.RxBytes ?? 0;
        var sent = profile.Status?.TxBytes ?? 0;
        if (!_counters.TryGetValue(profile.Id, out var previous))
        {
            _counters[profile.Id] = new Counters(now, received, sent);
            return;
        }

        var elapsed = now - previous.Time;

        // The reading stays as it was, so that the next one has a full interval to measure.
        if (elapsed < MinimumInterval)
        {
            return;
        }

        _counters[profile.Id] = new Counters(now, received, sent);

        // A counter that went down was reset: the interval says nothing.
        if (received < previous.Received || sent < previous.Sent)
        {
            _samples.Remove(profile.Id);
            return;
        }

        AddSample(profile.Id, new TrafficSample(++_nextId, now, (received - previous.Received) / elapsed.TotalSeconds, (sent - previous.Sent) / elapsed.TotalSeconds));
    }

    private void AddSample(string profileId, TrafficSample sample)
    {
        if (!_samples.TryGetValue(profileId, out var samples))
        {
            samples = [];
            _samples[profileId] = samples;
        }

        samples.Add(sample);
        if (samples.Count > Capacity)
        {
            samples.RemoveRange(0, samples.Count - Capacity);
        }
    }
}
