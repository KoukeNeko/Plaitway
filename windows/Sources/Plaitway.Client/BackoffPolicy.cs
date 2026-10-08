namespace Plaitway.Client;

/// <summary>
/// How long a dropped watch waits before it is retried: an exponential backoff with jitter. The numbers
/// are the ones of the macOS client, which notices a restarted daemon within seconds.
/// </summary>
/// <param name="Initial">The wait after the first failure.</param>
/// <param name="Max">The longest wait.</param>
/// <param name="Multiplier">What each further failure multiplies the wait by.</param>
/// <param name="Jitter">The wait varies by up to this fraction either way, so that clients do not retry in step.</param>
public sealed record BackoffPolicy(TimeSpan Initial, TimeSpan Max, double Multiplier, double Jitter)
{
    /// <summary>The policy the app uses: 200 ms, growing by 1.6 to 5 s, with 20 % jitter.</summary>
    public static BackoffPolicy Default { get; } = new(TimeSpan.FromMilliseconds(200), TimeSpan.FromSeconds(5), 1.6, 0.2);

    /// <summary>The wait after <paramref name="failures"/> failures in a row (1 is the first).</summary>
    /// <param name="failures">How many attempts failed in a row, at least 1.</param>
    /// <param name="random">A number in [0, 1), the source of the jitter.</param>
    public TimeSpan Delay(int failures, double random)
    {
        var grown = Initial.TotalMilliseconds * Math.Pow(Multiplier, Math.Max(failures, 1) - 1);
        var capped = Math.Min(grown, Max.TotalMilliseconds);
        var jittered = capped * (1 + (Jitter * ((2 * random) - 1)));
        return TimeSpan.FromMilliseconds(jittered);
    }
}
