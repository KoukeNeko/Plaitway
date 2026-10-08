using Microsoft.Extensions.Time.Testing;
using Plaitway.AppCore.Logs;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>macos/Tests/PlaitwayTests/App/ModelTests.swift TrafficHistoryTests, and the log cases of AppModelTests.swift that need no daemon.</summary>
public sealed class TrafficAndLogTests
{
    private static readonly DateTimeOffset Start = DateTimeOffset.FromUnixTimeSeconds(1_000);

    private static Profile Connected(ulong received, ulong sent, string id = "a")
    {
        var profile = Profiles.Make(id, state: ProfileState.Connected);
        profile.Status = new TunnelStatus { RxBytes = received, TxBytes = sent };
        return profile;
    }

    [Fact]
    public void WorksOutTheRatesFromTheCounters()
    {
        var history = new TrafficHistory();
        history.Record([Connected(1_000, 100)], Start);
        Assert.Null(history.RateOf("a"));

        history.Record([Connected(5_000, 300)], Start.AddSeconds(2));

        var rate = history.RateOf("a");
        Assert.Equal(2_000, rate?.Received);
        Assert.Equal(100, rate?.Sent);
        Assert.Single(history.SamplesOf("a"));
    }

    [Fact]
    public void ShowsTheMeanOfTheLastReadings()
    {
        var history = new TrafficHistory();

        // Counters every two seconds: 1000, 1000, 4000 and 4000 bytes per second.
        var counters = new ulong[] { 0, 2_000, 4_000, 12_000, 20_000 };
        for (var index = 0; index < counters.Length; index++)
        {
            history.Record([Connected(counters[index], 0)], Start.AddSeconds(index * 2));
        }

        Assert.Equal([1_000, 1_000, 4_000, 4_000], history.SamplesOf("a").Select(sample => sample.Received));

        // The last three samples only.
        Assert.Equal(3_000, history.RateOf("a")?.Received);
    }

    [Fact]
    public void IgnoresASecondReadingForTheSameInterval()
    {
        var history = new TrafficHistory();
        history.Record([Connected(0, 0)], Start);
        history.Record([Connected(10, 0)], Start.AddSeconds(0.2));
        Assert.Empty(history.SamplesOf("a"));

        // The interval is measured from the first reading, not from the one that was ignored.
        history.Record([Connected(2_000, 0)], Start.AddSeconds(2));

        Assert.Equal(1_000, history.RateOf("a")?.Received);
    }

    [Fact]
    public void StartsAgainWhenACounterGoesDown()
    {
        var history = new TrafficHistory();
        history.Record([Connected(1_000, 0)], Start);
        history.Record([Connected(3_000, 0)], Start.AddSeconds(2));
        Assert.Single(history.SamplesOf("a"));

        history.Record([Connected(100, 0)], Start.AddSeconds(4));
        Assert.Empty(history.SamplesOf("a"));

        // A reset says nothing about speed, and never a negative one.
        history.Record([Connected(2_100, 0)], Start.AddSeconds(6));
        Assert.Equal(1_000, history.RateOf("a")?.Received);
    }

    [Fact]
    public void ForgetsAProfileThatIsNotConnected()
    {
        var history = new TrafficHistory();
        history.Record([Connected(0, 0)], Start);
        history.Record([Connected(2_000, 0)], Start.AddSeconds(2));

        history.Record([Profiles.Make("a", state: ProfileState.Reconnecting)], Start.AddSeconds(4));
        Assert.Empty(history.SamplesOf("a"));

        history.Record([Connected(0, 0)], Start.AddSeconds(6));
        Assert.Null(history.RateOf("a"));
    }

    [Fact]
    public void KeepsTwoMinutes()
    {
        var history = new TrafficHistory();
        for (var step = 0; step <= TrafficHistory.Capacity + 20; step++)
        {
            history.Record([Connected((ulong)step * 1_000, 0)], Start.AddSeconds(step * 2));
        }

        Assert.Equal(TrafficHistory.Capacity, history.SamplesOf("a").Count);
    }

    [Fact]
    public void KeepsTheNewestLogLinesWithinItsCapacity()
    {
        var tail = new LogTail(TimeProvider.System);
        for (var number = 0; number < LogTail.Capacity + 500; number++)
        {
            tail.Append(new LogLine { Text = $"line {number}" });
        }

        Assert.InRange(tail.Entries.Count, LogTail.Capacity, LogTail.Capacity + 200);
        Assert.Equal($"line {LogTail.Capacity + 499}", tail.Entries[^1].Text);
        Assert.Equal(tail.Entries.Count, tail.Entries.Select(entry => entry.Id).Distinct().Count());
    }

    [Fact]
    public async Task ShowsLinesThatComeTogetherInOneUpdate()
    {
        var clock = new FakeTimeProvider();
        var tail = new LogTail(clock);
        var updates = 0;
        tail.PropertyChanged += (_, args) => updates += args.PropertyName == nameof(LogTail.Entries) ? 1 : 0;

        for (var number = 0; number < 100; number++)
        {
            tail.Enqueue(new LogLine { Text = $"line {number}" });
        }

        // The page is not drawn again for each line.
        Assert.Empty(tail.Entries);

        clock.Advance(TimeSpan.FromMilliseconds(60));
        await Wait.UntilAsync("the lines to be shown", () => tail.Entries.Count == 100);

        Assert.Equal("line 0", tail.Entries[0].Text);
        Assert.Equal(1, updates);
    }
}
