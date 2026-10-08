using System.Text;

namespace Plaitway.Client.Tests.Support;

/// <summary>Profile texts for the fake daemon, which reads <c># fake: ...</c> markers to decide how it behaves.</summary>
internal static class Fixture
{
    /// <summary>A profile in the style of the router profile the product has to handle.</summary>
    public static byte[] OpenVpn(string host = "vpn.example.net", IEnumerable<string>? markers = null, IEnumerable<string>? extra = null)
    {
        List<string> lines =
        [
            "client", "dev tun", "proto tcp-client", $"remote {host} 1194", "route 192.168.1.0 255.255.255.0",
            .. extra ?? [],
            .. markers ?? [],
            "<ca>", "-----BEGIN CERTIFICATE-----", "MIIBtestonlynotacertificate", "-----END CERTIFICATE-----", "</ca>",
        ];
        return Encoding.UTF8.GetBytes(string.Join('\n', lines) + "\n");
    }

    public static string OpenVpnText(string host = "vpn.example.net", IEnumerable<string>? markers = null, IEnumerable<string>? extra = null) =>
        Encoding.UTF8.GetString(OpenVpn(host, markers, extra));

    public static byte[] WireGuard(string allowedIps = "0.0.0.0/0", string endpoint = "203.0.113.5:51820") =>
        Encoding.UTF8.GetBytes(WireGuardText(allowedIps, endpoint));

    public static string WireGuardText(string allowedIps = "0.0.0.0/0", string endpoint = "203.0.113.5:51820") =>
        "[Interface]\nPrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nAddress = 10.6.0.2/32\nDNS = 10.6.0.1\n\n" +
        $"[Peer]\nPublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=\nAllowedIPs = {allowedIps}\nEndpoint = {endpoint}\n";
}
