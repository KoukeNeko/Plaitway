namespace Plaitway.Client.Tests.Support;

/// <summary>SplitMix64: the same sequence for the same seed on every run.</summary>
internal sealed class SeededRandom(ulong seed)
{
    private ulong _state = seed;

    public ulong Next()
    {
        _state += 0x9E3779B97F4A7C15;
        var z = _state;
        z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9;
        z = (z ^ (z >> 27)) * 0x94D049BB133111EB;
        return z ^ (z >> 31);
    }

    /// <summary>A number in [<paramref name="low"/>, <paramref name="high"/>], both included.</summary>
    public int Between(int low, int high) => low + (int)(Next() % (ulong)(high - low + 1));

    public bool Coin() => (Next() & 1) == 1;

    public T Pick<T>(IReadOnlyList<T> items) => items[Between(0, items.Count - 1)];
}
