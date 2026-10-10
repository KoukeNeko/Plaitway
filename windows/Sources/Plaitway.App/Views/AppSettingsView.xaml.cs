using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>The app's own settings and the helper.</summary>
internal sealed partial class AppSettingsView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(AppSettingsViewModel), typeof(AppSettingsView), new PropertyMetadata(null, (sender, _) => ((AppSettingsView)sender).Bindings.Update()));

    public AppSettingsView()
    {
        InitializeComponent();
    }

    public AppSettingsViewModel? ViewModel
    {
        get => (AppSettingsViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    /// <summary>Takes the user's choice of language; "nothing chosen", which the box reports while its list is replaced, is not one.</summary>
    private void OnLanguageChosen(object sender, SelectionChangedEventArgs args)
    {
        if (ViewModel is { } model && ((ComboBox)sender).SelectedItem is LanguageChoice choice)
        {
            model.SelectedLanguage = choice;
        }
    }
}
