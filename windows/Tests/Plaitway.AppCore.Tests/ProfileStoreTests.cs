using Grpc.Core;
using Microsoft.Extensions.Logging.Abstractions;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Tests.Support;
using Plaitway.Client;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>macos/Tests/PlaitwayTests/Client/ProfileStoreTests.swift: the store against the real daemon.</summary>
public sealed class ProfileStoreTests(DaemonBinary binary)
{
    [Fact]
    public async Task StartsEmptyAndKnowsTheDaemon()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            Assert.Empty(app.Store.Profiles);
            await Wait.UntilAsync("the daemon's description", () => app.Store.DaemonInfo is not null);
            Assert.True(app.Store.DaemonInfo!.Privileged);
            Assert.Equal([true, true], app.Store.DaemonInfo.Engines.Select(engine => engine.Available));
            Assert.NotEmpty(app.Store.DaemonInfo.Version);
        });
    }

    [Fact]
    public async Task ConnectsSeveralProfilesAtTheSameTime()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var office = await app.ImportFixtureAsync("office.ovpn");
            var lab = await app.ImportFixtureAsync("lab.ovpn");
            var home = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());

            await Task.WhenAll(
                app.Store.SetEnabledAsync(office, enabled: true),
                app.Store.SetEnabledAsync(lab, enabled: true),
                app.Store.SetEnabledAsync(home, enabled: true));

            await Wait.UntilAsync("three connected profiles", () => new[] { office, lab, home }.All(id => app.Store.Find(id)?.State == ProfileState.Connected));
            Assert.False(string.IsNullOrEmpty(app.Store.Find(home)?.Status.InterfaceName));
        });
    }

    [Fact]
    public async Task ReportsFailureWithAnError()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var broken = await app.ImportFixtureAsync("broken.ovpn", Fixture.OpenVpn(markers: ["# fake: fail"]));

            await app.Store.SetEnabledAsync(broken, enabled: true);
            await app.WaitForStateAsync(broken, ProfileState.Failed);

            Assert.NotEmpty(app.Store.Find(broken)!.LastError);
        });
    }

    [Fact]
    public async Task DisablingDisconnects()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var office = await app.ImportFixtureAsync("office.ovpn");

            await app.Store.SetEnabledAsync(office, enabled: true);
            await app.WaitForStateAsync(office, ProfileState.Connected);
            await app.Store.SetEnabledAsync(office, enabled: false);
            await app.WaitForStateAsync(office, ProfileState.Disconnected);

            Assert.False(app.Store.Find(office)?.DesiredEnabled);
        });
    }

    [Fact]
    public async Task UnknownProfileIsNotFound()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var error = await Assert.ThrowsAsync<RpcException>(() => app.Store.SetEnabledAsync("nope", enabled: true));

            Assert.Equal(StatusCode.NotFound, error.StatusCode);
            Assert.Equal(DaemonFailureKind.NotFound, DaemonFailure.From(error).Kind);
        });
    }

    [Fact]
    public async Task RecoversWhenTheDaemonRestarts()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var office = await app.ImportFixtureAsync("office.ovpn");
            await app.Store.SetEnabledAsync(office, enabled: true);
            await app.WaitForStateAsync(office, ProfileState.Connected);

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage to be noticed", () => app.Store.Connection == DaemonConnection.Unavailable);
            Assert.Equal(DaemonFailureKind.Unavailable, app.Store.UnavailableCause);

            // The store keeps the last known profiles instead of emptying the UI.
            Assert.Equal(ProfileState.Connected, app.Store.Find(office)?.State);

            await app.Daemon.StartAsync();
            await Wait.UntilAsync("the store to reconnect", () => app.Store.Connection == DaemonConnection.Connected);
            Assert.Null(app.Store.UnavailableCause);

            // The profile survives in the state directory; its tunnel does not.
            Assert.Equal(ProfileState.Disconnected, app.Store.Find(office)?.State);
            Assert.False(app.Store.Find(office)?.DesiredEnabled);
        });
    }

    [Fact]
    public async Task LaunchingTheAppDoesNotReconnectWhatTheUserDisconnected()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var flagged = (await app.Store.ImportProfileAsync(Fixture.OpenVpn(), "flagged.ovpn", settings: new ProfileSettings { AutoConnect = true })).Profile.Id;
            await Wait.UntilAsync("flagged to appear", () => app.Store.Find(flagged)?.Settings.AutoConnect == true);

            // The daemon connects auto-connect profiles when it starts; once the user disconnects one, it stays off.
            Assert.Equal(ProfileState.Disconnected, app.Store.Find(flagged)?.State);

            // The app's own store, as at launch.
            await using var api = new DaemonClientApi(DaemonClient.Connect(app.Daemon.Pipe));
            await using var launched = new ProfileStore(api, new InMemoryCredentialStore(), app.Ui, NullLogger<ProfileStore>.Instance);
            launched.Start();
            await Wait.UntilAsync("the first snapshot", () => launched.Connection == DaemonConnection.Connected);
            await Task.Delay(500);

            Assert.Equal(ProfileState.Disconnected, launched.Find(flagged)?.State);
            Assert.False(launched.Find(flagged)?.DesiredEnabled);
        });
    }
}
