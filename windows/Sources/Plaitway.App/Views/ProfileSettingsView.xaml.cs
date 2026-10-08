using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Plaitway.AppCore.ViewModels;
using Windows.System;

namespace Plaitway.App.Views;

/// <summary>The settings tab of a profile.</summary>
internal sealed partial class ProfileSettingsView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(ProfileSettingsViewModel), typeof(ProfileSettingsView), new PropertyMetadata(null, (sender, _) => ((ProfileSettingsView)sender).Bindings.Update()));

    public ProfileSettingsView()
    {
        InitializeComponent();
    }

    public ProfileSettingsViewModel? ViewModel
    {
        get => (ProfileSettingsViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    /// <summary>Takes the user's choice of mode; "nothing chosen", which the box reports while its list is replaced, is not one.</summary>
    private void OnTunnelModeChosen(object sender, SelectionChangedEventArgs args)
    {
        if (ViewModel is { } model && ((ComboBox)sender).SelectedItem is TunnelModeChoice choice)
        {
            model.SelectedTunnelMode = choice;
        }
    }

    // The daemon's reports must not overwrite a name that is being typed.
    private void OnNameGotFocus(object sender, RoutedEventArgs args)
    {
        if (ViewModel is { } model)
        {
            model.IsNameBeingEdited = true;
        }
    }

    private void OnNameLostFocus(object sender, RoutedEventArgs args)
    {
        if (ViewModel is { } model)
        {
            model.IsNameBeingEdited = false;
            model.Name = NameBox.Text;
            model.CommitNameCommand.Execute(null);
        }
    }

    private void OnNameKeyDown(object sender, KeyRoutedEventArgs args)
    {
        if (args.Key == VirtualKey.Enter && ViewModel is { } model)
        {
            args.Handled = true;
            model.Name = NameBox.Text;
            model.CommitNameCommand.Execute(null);
        }
    }
}
