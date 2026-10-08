using System.Net;
using System.Net.Sockets;

namespace Plaitway.Client.Config;

/// <summary>What a value of a profile looks like, decided with the strictness of <c>inet_pton</c>.</summary>
internal static class ValueShapes
{
    private const int Ipv4OctetCount = 4;
    private const int Ipv4MaxDigits = 3;
    private const int Ipv4MaxOctet = 255;

    /// <summary>True for decimal digits only; an empty text is not a number.</summary>
    public static bool IsNumber(string text) => text.Length > 0 && text.All(character => character is >= '0' and <= '9');

    /// <summary>
    /// True for a plain IPv4 or IPv6 address. Unlike <see cref="IPAddress.TryParse(string, out IPAddress)"/>,
    /// <c>1</c>, <c>10.1</c>, <c>010.0.0.1</c>, <c>[::1]</c> and <c>fe80::1%eth0</c> are not addresses, as for
    /// <c>inet_pton</c>, which is what the macOS tokenizer asks.
    /// </summary>
    public static bool IsIPAddress(string text) => IsIPv4(text) || IsIPv6(text);

    /// <summary><see cref="ConfigTokenRole.IPAddress"/> for an address, <see cref="ConfigTokenRole.Cidr"/> for an address with a prefix length, null for neither.</summary>
    public static ConfigTokenRole? AddressRole(string text)
    {
        if (IsIPAddress(text))
        {
            return ConfigTokenRole.IPAddress;
        }

        var slash = text.LastIndexOf('/');
        return slash >= 0 && IsIPAddress(text[..slash]) && IsNumber(text[(slash + 1)..]) ? ConfigTokenRole.Cidr : null;
    }

    private static bool IsIPv4(string text)
    {
        var octets = text.Split('.');
        return octets.Length == Ipv4OctetCount && octets.All(IsIPv4Octet);
    }

    private static bool IsIPv4Octet(string octet) =>
        octet.Length is > 0 and <= Ipv4MaxDigits
        && IsNumber(octet)
        && (octet.Length == 1 || octet[0] != '0')
        && int.Parse(octet, System.Globalization.CultureInfo.InvariantCulture) <= Ipv4MaxOctet;

    private static bool IsIPv6(string text) =>
        text.Contains(':')
        && text.All(character => character is not ('[' or ']' or '%' or '/' or ' '))
        && HasStrictIPv4Tail(text)
        && IPAddress.TryParse(text, out var address)
        && address.AddressFamily == AddressFamily.InterNetworkV6;

    /// <summary>
    /// False for an embedded IPv4 tail that is not a strict IPv4 address (<c>::ffff:1.2.3.04</c>), which
    /// <see cref="IPAddress.TryParse(string, out IPAddress)"/> takes but <c>inet_pton</c> and Go refuse.
    /// </summary>
    private static bool HasStrictIPv4Tail(string text)
    {
        var tail = text[(text.LastIndexOf(':') + 1)..];
        return !tail.Contains('.') || IsIPv4(tail);
    }
}
