using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>Chooses the view of a page by the type of its view model.</summary>
internal sealed class PageTemplateSelector : DataTemplateSelector
{
    /// <summary>The page of a profile.</summary>
    public DataTemplate? Profile { get; set; }

    /// <summary>The Diagnostics page.</summary>
    public DataTemplate? Diagnostics { get; set; }

    /// <summary>The page of the app's settings.</summary>
    public DataTemplate? Settings { get; set; }

    /// <summary>The page of a window with no profile.</summary>
    public DataTemplate? Empty { get; set; }

    /// <inheritdoc />
    protected override DataTemplate? SelectTemplateCore(object item) => item switch
    {
        ProfilePageViewModel => Profile,
        DiagnosticsViewModel => Diagnostics,
        AppSettingsViewModel => Settings,
        EmptyProfilesViewModel => Empty,
        _ => null,
    };

    /// <inheritdoc />
    protected override DataTemplate? SelectTemplateCore(object item, DependencyObject container) => SelectTemplateCore(item);
}
