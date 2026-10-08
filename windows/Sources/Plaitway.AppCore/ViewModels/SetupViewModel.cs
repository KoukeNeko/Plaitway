using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;

namespace Plaitway.AppCore.ViewModels;

/// <summary>What the window shows instead of the profiles while the helper is not ready: one thing to do about it.</summary>
public sealed partial class SetupViewModel : ObservableObject, IDisposable
{
    private readonly AppModel _model;
    private SetupAction? _primary;
    private SetupAction? _secondary;

    /// <summary>Makes the page; it follows the model.</summary>
    public SetupViewModel(AppModel model)
    {
        _model = model;
        _model.PropertyChanged += OnModelChanged;
        Refresh();
    }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>There is something to show instead of the profiles.</summary>
    [ObservableProperty]
    public partial bool IsShown { get; private set; }

    /// <summary>The glyph above the title.</summary>
    [ObservableProperty]
    public partial StatusIcon Icon { get; private set; }

    /// <summary>What is wrong.</summary>
    [ObservableProperty]
    public partial string Title { get; private set; } = string.Empty;

    /// <summary>What the user needs to know to act; may be a path or a command to copy.</summary>
    [ObservableProperty]
    public partial string Detail { get; private set; } = string.Empty;

    /// <summary>There is a detail.</summary>
    public bool HasDetail => Detail.Length > 0;

    /// <summary>Something is under way.</summary>
    [ObservableProperty]
    public partial bool ShowsProgress { get; private set; }

    /// <summary>The label of the main button; empty when there is none.</summary>
    [ObservableProperty]
    public partial string PrimaryLabel { get; private set; } = string.Empty;

    /// <summary>There is a main button.</summary>
    public bool HasPrimary => _primary is not null;

    /// <summary>The label of the second button; empty when there is none.</summary>
    [ObservableProperty]
    public partial string SecondaryLabel { get; private set; } = string.Empty;

    /// <summary>There is a second button.</summary>
    public bool HasSecondary => _secondary is not null;

    /// <inheritdoc />
    public void Dispose() => _model.PropertyChanged -= OnModelChanged;

    /// <summary>Does the main thing.</summary>
    [RelayCommand]
    public Task PrimaryAsync() => _primary is { } action ? _model.PerformAsync(action) : Task.CompletedTask;

    /// <summary>Does the other thing.</summary>
    [RelayCommand]
    public Task SecondaryAsync() => _secondary is { } action ? _model.PerformAsync(action) : Task.CompletedTask;

    private void OnModelChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName is nameof(AppModel.Setup) or nameof(AppModel.KeepsProfilesInView))
        {
            Refresh();
        }
    }

    private void Refresh()
    {
        var content = _model.Setup.Content(Text);
        IsShown = content is not null && !_model.KeepsProfilesInView;
        Icon = content?.Icon ?? StatusIcon.Idle;
        Title = content?.Title ?? string.Empty;
        Detail = content?.Detail ?? string.Empty;
        ShowsProgress = content?.ShowsProgress ?? false;
        _primary = content?.Primary;
        _secondary = content?.Secondary;
        PrimaryLabel = _primary is { } primary ? SetupContent.Label(primary, Text) : string.Empty;
        SecondaryLabel = _secondary is { } secondary ? SetupContent.Label(secondary, Text) : string.Empty;
        OnPropertyChanged(nameof(HasDetail));
        OnPropertyChanged(nameof(HasPrimary));
        OnPropertyChanged(nameof(HasSecondary));
    }
}
