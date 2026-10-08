using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.App.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>The overview tab of a profile.</summary>
internal sealed partial class OverviewView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(OverviewViewModel), typeof(OverviewView), new PropertyMetadata(null, (sender, _) => ((OverviewView)sender).Bindings.Update()));

    private readonly DispatcherTimerClock _clock;

    public OverviewView()
    {
        InitializeComponent();
        _clock = new DispatcherTimerClock(DispatcherQueue, () => ViewModel?.RefreshUptime());
        Loaded += (_, _) => _clock.Start();
        Unloaded += (_, _) => _clock.Stop();
    }

    public OverviewViewModel? ViewModel
    {
        get => (OverviewViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }
}
