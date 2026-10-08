using System.Diagnostics.CodeAnalysis;
using Microsoft.UI.Xaml;
using Plaitway.App.Diagnostics;
using Plaitway.App.Startup;
using Plaitway.Client.Transport;

namespace Plaitway.App;

/// <summary>
/// The application. Starting it makes the <see cref="AppHost"/>, which puts everything together; ending it is the host's
/// work too, once the user has said what the profiles that are on should do.
/// </summary>
[SuppressMessage("Design", "CA1001", Justification = "The application lives as long as the process; AppHost.ShutDownAsync disposes what it owns before the process ends.")]
public partial class App : Application
{
    private const int InvalidPipeExitCode = 2;

    private readonly RunReport _report = new();
    private AppHost? _host;

    public App()
    {
        InitializeComponent();
        UnhandledException += (_, args) => _report.Write("unhandled", args.Exception);
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        try
        {
            var options = AppOptions.Parse([.. Environment.GetCommandLineArgs().Skip(1)], Environment.GetEnvironmentVariable);
            _host = AppHost.Start(this, options, _report);
            if (_host is null)
            {
                // The copy that is running was asked to come forward.
                Exit();
            }
        }
        catch (InvalidPipePathException error)
        {
            _report.Write("error", error.Message);
            Environment.ExitCode = InvalidPipeExitCode;
            Exit();
        }
        catch (Exception error)
        {
            // A failed start would otherwise end as a crash that says nothing of its cause.
            _report.Write("startup_error", error);
            throw;
        }
    }
}
