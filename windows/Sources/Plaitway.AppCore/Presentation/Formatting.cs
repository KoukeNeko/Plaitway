using System.Globalization;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

/// <summary>Numbers, endpoints and times as the pages show them.</summary>
public static class Formatting
{
    private const decimal DecimalUnit = 1000m;
    private const decimal OneDecimalBelow = 10m;
    private const int WholeNumber = 0;
    private const int OneDecimal = 1;
    private static readonly string[] ByteUnits = ["B", "KB", "MB", "GB", "TB", "PB", "EB"];
    private const string LogTimeFormat = "HH:mm:ss";

    /// <summary>Decimal units, as Explorer counts them: 1.5 MB, 15 MB, 150 MB.</summary>
    public static string Bytes(ulong count, CultureInfo culture)
    {
        // Decimal arithmetic: a count just below a boundary must round the way it is shown, and a double cannot tell
        // 999.95 from the number next to it.
        var value = (decimal)count;
        var unit = 0;
        while (unit < ByteUnits.Length - 1 && RoundedForDisplay(value, unit) >= DecimalUnit)
        {
            value /= DecimalUnit;
            unit++;
        }

        var shown = RoundedForDisplay(value, unit);
        var digits = DigitsShown(shown, unit) == OneDecimal ? "0.0" : "0";
        return shown.ToString(digits, culture) + " " + ByteUnits[unit];
    }

    /// <summary>The value as the label will print it, so that 999.5 KB, which prints as 1000, is already a megabyte.</summary>
    private static decimal RoundedForDisplay(decimal value, int unit)
    {
        var oneDecimal = unit > 0 && Math.Round(value, OneDecimal, MidpointRounding.AwayFromZero) < OneDecimalBelow;
        return Math.Round(value, oneDecimal ? OneDecimal : WholeNumber, MidpointRounding.AwayFromZero);
    }

    private static int DigitsShown(decimal rounded, int unit) => unit > 0 && rounded < OneDecimalBelow ? OneDecimal : WholeNumber;

    /// <summary>A rate, as "1.5 MB/s".</summary>
    public static string Rate(double bytesPerSecond, UiText text) =>
        text.PerS(Bytes((ulong)Math.Max(Math.Round(bytesPerSecond), 0), text.Culture));

    /// <summary>"host:port (tcp)" for an endpoint of a profile.</summary>
    public static string Endpoint(string host, uint port, string protocol)
    {
        var address = port == 0 ? host : $"{host}:{port.ToString(CultureInfo.InvariantCulture)}";
        return protocol.Length == 0 ? address : $"{address} ({protocol})";
    }

    /// <summary>The endpoint of a profile as <see cref="Endpoint(string, uint, string)"/> shows it.</summary>
    public static string Endpoint(Endpoint endpoint) => Endpoint(endpoint.Host, endpoint.Port, endpoint.Protocol);

    /// <summary>A time of day that is the same width in every language, so that log lines line up.</summary>
    public static string LogTime(DateTimeOffset time) => time.ToLocalTime().ToString(LogTimeFormat, CultureInfo.InvariantCulture);
}
