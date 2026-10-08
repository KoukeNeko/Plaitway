using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.Tray;

/// <summary>What the tray icon asks the window to do.</summary>
public enum ShellRequest
{
    /// <summary>Show the window and bring it forward.</summary>
    ShowWindow,

    /// <summary>Show the window and ask for profile files.</summary>
    ImportProfile,

    /// <summary>Show the window on the app's settings.</summary>
    ShowSettings,

    /// <summary>End the app, after asking what the profiles should do.</summary>
    Quit,
}

/// <summary>
/// The notification-area icon: how all profiles stand together, as a state for the icon and a line for its tooltip, and the
/// menu that opens from it. It changes only when what it shows changes: a traffic counter changes the profiles every second
/// or two, and a menu that is redrawn for it would move under the pointer.
/// </summary>
public sealed partial class TrayViewModel : ObservableObject, IDisposable
{
    private readonly AppModel _model;
    private MenuModel? _shownMenu;

    /// <summary>Makes the icon's model; it follows <paramref name="model"/>.</summary>
    public TrayViewModel(AppModel model)
    {
        _model = model;
        _model.PropertyChanged += OnChanged;
        _model.Store.PropertyChanged += OnChanged;
        Refresh();
    }

    /// <summary>The icon asks for something.</summary>
    public event Action<ShellRequest>? Requested;

    private UiText Text => _model.Text;

    /// <summary>How the profiles stand together.</summary>
    [ObservableProperty]
    public partial AggregateState State { get; private set; }

    /// <summary>The state in a word, which is what the tooltip and the screen reader say after the app's name.</summary>
    [ObservableProperty]
    public partial string StateLabel { get; private set; } = string.Empty;

    /// <summary>The tooltip: the app's name, then the state.</summary>
    public string Tooltip => $"{AppIdentity.ProductName} · {StateLabel}";

    /// <summary>The entries of the menu; a new list only when the menu changed.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<TrayEntry> Entries { get; private set; } = [];

    /// <inheritdoc />
    public void Dispose()
    {
        _model.PropertyChanged -= OnChanged;
        _model.Store.PropertyChanged -= OnChanged;
    }

    /// <summary>Does what choosing <paramref name="entry"/> says; the profile rows do what their profile needs.</summary>
    public async Task InvokeAsync(TrayEntry entry)
    {
        switch (entry.Command)
        {
            case TrayCommand.ChooseProfile when entry.ProfileId is { } profileId:
                await ChooseProfileAsync(profileId);
                break;
            case TrayCommand.DisconnectAll:
                _ = await _model.DisconnectAllAsync();
                break;
            case TrayCommand.Open:
                Requested?.Invoke(ShellRequest.ShowWindow);
                break;
            case TrayCommand.Import when _model.Setup.IsUsable:
                Requested?.Invoke(ShellRequest.ImportProfile);
                break;
            case TrayCommand.Settings:
                Requested?.Invoke(ShellRequest.ShowSettings);
                break;
            case TrayCommand.Quit:
                Requested?.Invoke(ShellRequest.Quit);
                break;
            default:
                break;
        }
    }

    /// <summary>The icon was clicked: the window comes forward.</summary>
    public void Select() => Requested?.Invoke(ShellRequest.ShowWindow);

    private async Task ChooseProfileAsync(string profileId)
    {
        if (_model.Store.Find(profileId) is not { } profile)
        {
            return;
        }

        switch (MenuModel.ItemFor(profile, Text).Action)
        {
            case MenuAction.Connect or MenuAction.Retry:
                await _model.SetEnabledAsync(profileId, enabled: true);
                break;
            case MenuAction.Disconnect:
                await _model.SetEnabledAsync(profileId, enabled: false);
                break;
            default:
                Requested?.Invoke(ShellRequest.ShowWindow);
                break;
        }
    }

    private void OnChanged(object? sender, PropertyChangedEventArgs args) => Refresh();

    private void Refresh()
    {
        var state = AggregateStates.Of(_model.Setup, _model.Store.Profiles);
        State = state;
        StateLabel = state.Label(Text);
        OnPropertyChanged(nameof(Tooltip));

        var menu = new MenuModel(_model.Setup, _model.Store.Profiles, Text);
        if (!menu.Equals(_shownMenu))
        {
            _shownMenu = menu;
            Entries = TrayMenuBuilder.Build(menu, Text);
        }
    }
}
