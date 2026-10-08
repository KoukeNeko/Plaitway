using System.Text;
using Plaitway.Client;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.App.UiTests.Support;

/// <summary>A daemon on its fake backend with the profiles a window has something to show for, and a client to look at it.</summary>
internal sealed class World : IAsyncDisposable
{
    /// <summary>An OpenVPN profile that the fake backend connects.</summary>
    public const string Office = "Office";

    /// <summary>A WireGuard profile that the fake backend connects.</summary>
    public const string Home = "Home";

    /// <summary>An OpenVPN profile that is left disconnected.</summary>
    public const string Lab = "Lab";

    private static readonly TimeSpan ConnectTimeout = TimeSpan.FromSeconds(20);

    private readonly DaemonProcess _daemon;

    private World(DaemonProcess daemon)
    {
        _daemon = daemon;
        Client = DaemonClient.Connect(daemon.Pipe);
    }

    /// <summary>The daemon, as the test asks it.</summary>
    public DaemonClient Client { get; }

    /// <summary>The pipe the app is told to use.</summary>
    public string Pipe => _daemon.PipeName;

    /// <summary>Starts the daemon and imports the three profiles; <paramref name="connected"/> are switched on.</summary>
    public static async Task<World> StartAsync(DaemonBinary binary, params string[] connected)
    {
        var world = new World(await DaemonProcess.StartAsync(binary));
        try
        {
            await world.ImportAsync(Office, "Office.ovpn", Fixture.OpenVpn());
            await world.ImportAsync(Home, "Home.conf", Fixture.WireGuard());
            await world.ImportAsync(Lab, "Lab.ovpn", Fixture.OpenVpn("lab.example.net"));
            foreach (var name in connected)
            {
                await world.ConnectAsync(name);
            }

            return world;
        }
        catch
        {
            await world.DisposeAsync();
            throw;
        }
    }

    /// <summary>The profile as the daemon holds it now.</summary>
    public async Task<Profile> ProfileAsync(string name) =>
        (await Client.ListProfilesAsync()).Single(profile => profile.Name == name);

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        await Client.DisposeAsync();
        await _daemon.DisposeAsync();
    }

    private async Task ImportAsync(string name, string filename, byte[] content) =>
        await Client.ImportProfileAsync(content, filename, name);

    private async Task ConnectAsync(string name)
    {
        var profile = await ProfileAsync(name);
        await Client.SetProfileEnabledAsync(profile.Id, enabled: true);
        await Wait.UntilAsync($"{name} to connect", async () => (await ProfileAsync(name)).State == ProfileState.Connected, ConnectTimeout);
    }
}

/// <summary>Waiting for a window or a process to do something, because UI Automation and the shell answer when they answer.</summary>
internal static class Wait
{
    private static readonly TimeSpan DefaultTimeout = TimeSpan.FromSeconds(15);
    private static readonly TimeSpan Poll = TimeSpan.FromMilliseconds(100);

    /// <summary>Waits until <paramref name="condition"/> holds.</summary>
    public static Task UntilAsync(string what, Func<bool> condition, TimeSpan? timeout = null) =>
        UntilAsync(what, () => Task.FromResult(condition()), timeout);

    /// <summary>Waits until <paramref name="condition"/> holds.</summary>
    public static async Task UntilAsync(string what, Func<Task<bool>> condition, TimeSpan? timeout = null)
    {
        var deadline = DateTime.UtcNow + (timeout ?? DefaultTimeout);
        while (!await condition())
        {
            if (DateTime.UtcNow > deadline)
            {
                throw new TimeoutException($"timed out waiting for {what}");
            }

            await Task.Delay(Poll);
        }
    }
}
