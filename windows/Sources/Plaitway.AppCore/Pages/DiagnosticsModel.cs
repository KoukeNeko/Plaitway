using CommunityToolkit.Mvvm.ComponentModel;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Presentation;
using Plaitway.V1;

namespace Plaitway.AppCore.Pages;

/// <summary>What the Diagnostics page shows, read from the daemon while the page is open.</summary>
/// <param name="store">Where to read from.</param>
/// <param name="errors">Words for a failed read.</param>
/// <param name="time">The clock of the polling.</param>
public sealed partial class DiagnosticsModel(ProfileStore store, ErrorText errors, TimeProvider time) : ObservableObject
{
    private static readonly TimeSpan RefreshInterval = TimeSpan.FromSeconds(2);

    /// <summary>The latest reading; null until there is one.</summary>
    [ObservableProperty]
    public partial Plaitway.V1.Diagnostics? Reading { get; private set; }

    /// <summary>The latest read or command failed; the previous reading stays on screen.</summary>
    [ObservableProperty]
    public partial string? Failure { get; private set; }

    /// <summary>
    /// Reads until <paramref name="cancellationToken"/> is cancelled: routes change with connections and network
    /// changes, neither of which the profile watch reports.
    /// </summary>
    public async Task RunAsync(CancellationToken cancellationToken)
    {
        try
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                await RefreshAsync(cancellationToken);
                await Task.Delay(RefreshInterval, time, cancellationToken);
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // The page was left.
        }
    }

    /// <summary>Reads once.</summary>
    public async Task RefreshAsync(CancellationToken cancellationToken = default)
    {
        try
        {
            var reading = await store.FetchDiagnosticsAsync(cancellationToken);

            // Most readings equal the last one; assigning would redraw the page for nothing.
            if (!reading.Equals(Reading))
            {
                Reading = reading;
            }

            Failure = null;
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // The page was left; a failure now says nothing.
        }
        catch (Exception error)
        {
            Failure = errors.UserMessage(error);
        }
    }
}
