using Microsoft.Extensions.Logging.Abstractions;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Text;
using Plaitway.Client;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>What a test wants different from the usual daemon and helper.</summary>
internal sealed record HarnessOptions
{
    /// <summary>The state of the helper service.</summary>
    public HelperState HelperState { get; init; } = HelperState.Running;

    /// <summary>Whether plaitwayd.exe is where the app looks.</summary>
    public bool HasExecutable { get; init; } = true;

    /// <summary>The version of the app, when the test cares.</summary>
    public string? AppVersion { get; init; }

    /// <summary>The daemon is the developer's.</summary>
    public bool IsOverridden { get; init; }

    /// <summary>Where saved credentials go; in memory when null.</summary>
    public ICredentialStore? Credentials { get; init; }

    /// <summary>The daemon's in-memory backend; false for its real engines.</summary>
    public bool Fake { get; init; } = true;
}

/// <summary>
/// The daemon, the store and the model, connected and running on the test UI thread, torn down after the test. The
/// body of a test goes through <see cref="RunAsync"/> so that it, like the app, is on the thread that owns the model.
/// </summary>
internal sealed class AppHarness : IAsyncDisposable
{
    private static readonly BackoffPolicy FastBackoff = new(TimeSpan.FromMilliseconds(20), TimeSpan.FromMilliseconds(250), 1.6, 0.2);

    private AppHarness(DaemonProcess daemon, HarnessOptions options)
    {
        Daemon = daemon;
        Ui = new UiThread();
        Credentials = options.Credentials ?? new InMemoryCredentialStore();
        Service = new FakeHelperService { State = options.HelperState };
        Launcher = new FakeElevatedLauncher();
        Startup = new FakeStartup();
        Text = TestText.En;
        Api = new DaemonClientApi(DaemonClient.Connect(daemon.Pipe, FastBackoff));
        Calls = new RecordingDaemonApi(Api);
        Trust = new FakeHelperTrust();
        var locator = new HelperLocator(@"C:\Plaitway\app", _ => options.HasExecutable, Trust, searchesRepository: false);
        Installer = new HelperInstaller(Service, Launcher, locator);
        Store = new ProfileStore(Calls, Credentials, Ui, NullLogger<ProfileStore>.Instance);
        Model = new AppModel(
            Store, Installer, Startup, Text, new AppEnvironment(options.AppVersion, options.IsOverridden), TimeProvider.System, NullLogger<AppModel>.Instance);
    }

    public DaemonProcess Daemon { get; }

    public UiThread Ui { get; }

    public ICredentialStore Credentials { get; }

    public FakeHelperService Service { get; }

    public FakeElevatedLauncher Launcher { get; }

    public FakeStartup Startup { get; }

    public UiText Text { get; }

    public DaemonClientApi Api { get; }

    /// <summary>What the store asked the daemon.</summary>
    public RecordingDaemonApi Calls { get; }

    /// <summary>What Windows says about the helper file; trusted unless a test says otherwise.</summary>
    public FakeHelperTrust Trust { get; }

    public HelperInstaller Installer { get; }

    public ProfileStore Store { get; }

    public AppModel Model { get; }

    /// <summary>Starts a daemon and the app's model on it, and waits for the first snapshot.</summary>
    public static async Task<AppHarness> StartAsync(DaemonBinary binary, HarnessOptions? options = null)
    {
        options ??= new HarnessOptions();
        var daemon = await DaemonProcess.StartAsync(binary, options.Fake);
        var harness = new AppHarness(daemon, options);
        try
        {
            await harness.RunAsync(async () =>
            {
                harness.Model.Start();
                await Wait.UntilAsync("the first snapshot", () => harness.Store.Connection == DaemonConnection.Connected);
            });
            return harness;
        }
        catch
        {
            await harness.DisposeAsync();
            throw;
        }
    }

    /// <summary>Runs the body of a test on the UI thread.</summary>
    public Task RunAsync(Func<Task> body) => Ui.RunAsync(body);

    /// <summary>Imports a profile and waits until the store shows it; returns its id.</summary>
    public async Task<string> ImportFixtureAsync(string filename, byte[]? content = null)
    {
        var imported = await Store.ImportProfileAsync(content ?? Fixture.OpenVpn(), filename);
        var id = imported.Profile.Id;
        await Wait.UntilAsync($"{filename} to appear", () => Store.Find(id) is not null);
        return id;
    }

    public Task WaitForStateAsync(string id, ProfileState state) =>
        Wait.UntilAsync($"{id} to be {state}", () => Store.Find(id)?.State == state);

    public async ValueTask DisposeAsync()
    {
        await Ui.RunAsync(async () =>
        {
            await Model.DisposeAsync();
            await Api.DisposeAsync();
        });
        Ui.Dispose();
        await Daemon.DisposeAsync();
    }
}
