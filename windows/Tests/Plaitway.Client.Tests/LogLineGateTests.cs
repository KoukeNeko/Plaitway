using Google.Protobuf.WellKnownTypes;
using Plaitway.V1;

namespace Plaitway.Client.Tests;

public sealed class LogLineGateTests
{
    private static LogLine At(long seconds, int nanos, string text = "", LogLevel level = LogLevel.Info) => new()
    {
        Text = text,
        Level = level,
        Time = new Timestamp { Seconds = seconds, Nanos = nanos },
    };

    private static List<string> Admitted(LogLineGate gate, params LogLine[] lines) =>
        [.. lines.Where(gate.Admit).Select(line => line.Text)];

    [Fact]
    public void DeliversEveryLineOfAStreamThatWasNeverLost()
    {
        var gate = new LogLineGate();
        // The clock ticks slower than the daemon logs: a hundred lines, three times, and some out of order.
        var burst = Enumerable.Range(0, 100).Select(index => At(10, 500, "line " + index)).ToArray();

        var delivered = new[] { Admitted(gate, burst), Admitted(gate, burst), Admitted(gate, At(9, 0, "older")) };

        Assert.Equal(100, delivered[0].Count);
        Assert.Equal(100, delivered[1].Count);
        Assert.Equal(["older"], delivered[2]);
    }

    [Fact]
    public void SkipsWhatAReplayRepeatsAndDeliversWhatIsNew()
    {
        var gate = new LogLineGate();
        Admitted(gate, At(10, 0, "a"), At(10, 1, "b"), At(11, 0, "c"));

        gate.StreamLost();
        var again = Admitted(gate, At(10, 0, "a"), At(10, 1, "b"), At(11, 0, "c"), At(11, 5, "d"));

        Assert.Equal(["d"], again);
    }

    [Fact]
    public void SkipsAsManyLinesAtTheNewestTimeAsWereDelivered()
    {
        var gate = new LogLineGate();
        Admitted(gate, At(10, 0, "a"), At(11, 0, "b"), At(11, 0, "c"), At(11, 0, "c"));

        gate.StreamLost();
        // The replay has the same group and two lines more that arrived while the stream was down: one with
        // the text of a delivered line, one without.
        var again = Admitted(gate, At(10, 0, "a"), At(11, 0, "b"), At(11, 0, "c"), At(11, 0, "c"), At(11, 0, "c"), At(11, 0, "x"), At(12, 0, "y"));

        Assert.Equal(["c", "x", "y"], again);
    }

    [Fact]
    public void TellsLinesOfOneTimeApartByTextAndLevel()
    {
        var gate = new LogLineGate();
        Admitted(gate, At(5, 0, "same", LogLevel.Info));

        gate.StreamLost();
        var again = Admitted(gate, At(5, 0, "same", LogLevel.Error), At(5, 0, "same", LogLevel.Info), At(5, 0, "same", LogLevel.Info));

        Assert.Equal(["same", "same"], again);
    }

    [Fact]
    public void ARepeatedTailThatIsCutInTheMiddleOfAGroupIsStillSkipped()
    {
        var gate = new LogLineGate();
        Admitted(gate, At(1, 0, "a"), At(1, 0, "b"), At(1, 0, "c"), At(1, 0, "d"));

        gate.StreamLost();
        var again = Admitted(gate, At(1, 0, "c"), At(1, 0, "d"), At(1, 0, "e"));

        Assert.Equal(["e"], again);
    }

    [Fact]
    public void TheReplayEndsAtTheFirstLineNewerThanEverythingDelivered()
    {
        var gate = new LogLineGate();
        Admitted(gate, At(1, 0, "a"));

        gate.StreamLost();
        var after = Admitted(gate, At(1, 0, "a"), At(2, 0, "b"), At(2, 0, "a"), At(1, 0, "a"));

        // After "b" the stream is live again, and every line is a new one.
        Assert.Equal(["b", "a", "a"], after);
    }

    [Fact]
    public void ALossDuringAReplayStartsTheReplayAgain()
    {
        var gate = new LogLineGate();
        Admitted(gate, At(1, 0, "a"), At(1, 0, "b"));
        gate.StreamLost();
        Assert.Empty(Admitted(gate, At(1, 0, "a")));

        gate.StreamLost();
        var again = Admitted(gate, At(1, 0, "a"), At(1, 0, "b"), At(1, 0, "c"));

        Assert.Equal(["c"], again);
    }

    [Fact]
    public void ALossBeforeAnyLineSkipsNothing()
    {
        var gate = new LogLineGate();

        gate.StreamLost();

        Assert.Equal(["a", "a"], Admitted(gate, At(1, 0, "a"), At(1, 0, "a")));
    }

    [Fact]
    public void ALineWithoutATimeIsAlwaysDelivered()
    {
        var gate = new LogLineGate();
        Admitted(gate, At(5, 5, "timed"));
        gate.StreamLost();

        var untimed = Admitted(gate, new LogLine { Text = "no time" }, new LogLine { Text = "no time" });

        Assert.Equal(["no time", "no time"], untimed);
        Assert.Empty(Admitted(gate, At(5, 5, "timed")));
    }
}
