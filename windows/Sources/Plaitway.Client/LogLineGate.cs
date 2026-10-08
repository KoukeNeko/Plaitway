using Google.Protobuf.WellKnownTypes;
using Plaitway.V1;

namespace Plaitway.Client;

/// <summary>
/// Lets a log line through once. A log watch that was lost starts again with the daemon's buffered tail,
/// which repeats lines that were delivered already; <see cref="StreamLost"/> marks that, and until a line
/// newer than everything delivered arrives, the repeats are skipped.
/// </summary>
/// <remarks>
/// During such a replay a line is a repeat when it is older than the newest line delivered, or as new and
/// one of the lines delivered at that very time has its text and level. Times alone cannot decide this: the
/// daemon stamps lines with its wall clock, which ticks every 0.3 to 1 ms on Windows, so several lines
/// share a time. Outside a replay every line is delivered, whatever its time.
/// </remarks>
internal sealed class LogLineGate
{
    private readonly List<LineIdentity> _deliveredAtNewest = [];
    private Timestamp? _newest;

    /// <summary>While a replay is expected: the lines delivered at the newest time that it has not shown again.</summary>
    private List<LineIdentity>? _awaitedInReplay;

    /// <summary>The stream was lost: the lines that follow may repeat what was delivered.</summary>
    public void StreamLost() => _awaitedInReplay = [.. _deliveredAtNewest];

    /// <summary>True for a line to deliver. A line without a time is always delivered: nothing says it was.</summary>
    public bool Admit(LogLine line)
    {
        if (line.Time is null)
        {
            return true;
        }

        if (_awaitedInReplay is not null && _newest is not null)
        {
            var order = Compare(line.Time, _newest);
            if (order < 0 || (order == 0 && TakeAwaited(line)))
            {
                return false;
            }

            if (order > 0)
            {
                _awaitedInReplay = null;
            }
        }

        Record(line);
        return true;
    }

    /// <summary>True when the line is one delivered before; the replay then has one repeat fewer to show.</summary>
    private bool TakeAwaited(LogLine line)
    {
        var index = _awaitedInReplay!.IndexOf(LineIdentity.Of(line));
        if (index < 0)
        {
            return false;
        }

        _awaitedInReplay.RemoveAt(index);
        return true;
    }

    private void Record(LogLine line)
    {
        var order = _newest is null ? 1 : Compare(line.Time, _newest);
        if (order > 0)
        {
            _newest = line.Time;
            _deliveredAtNewest.Clear();
        }

        // A line older than the newest was delivered out of order; it says nothing about the next one.
        if (order >= 0)
        {
            _deliveredAtNewest.Add(LineIdentity.Of(line));
        }
    }

    private static int Compare(Timestamp left, Timestamp right) => (left.Seconds, left.Nanos).CompareTo((right.Seconds, right.Nanos));

    private readonly record struct LineIdentity(string Text, LogLevel Level)
    {
        public static LineIdentity Of(LogLine line) => new(line.Text, line.Level);
    }
}
