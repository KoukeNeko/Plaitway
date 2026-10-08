using System.ComponentModel;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Plaitway.AppCore.Editing;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>The configuration tab of a profile: its text, with the secrets hidden until asked for.</summary>
internal sealed partial class ConfigurationView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(ConfigurationViewModel), typeof(ConfigurationView), new PropertyMetadata(null, (sender, args) => ((ConfigurationView)sender).OnViewModelChanged(args)));

    public ConfigurationView()
    {
        InitializeComponent();
    }

    public ConfigurationViewModel? ViewModel
    {
        get => (ConfigurationViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    private void OnViewModelChanged(DependencyPropertyChangedEventArgs args)
    {
        Bindings.Update();
        if (args.OldValue is ConfigurationViewModel old)
        {
            old.PropertyChanged -= OnViewModelPropertyChanged;
        }

        if (args.NewValue is ConfigurationViewModel current)
        {
            current.PropertyChanged += OnViewModelPropertyChanged;
        }
    }

    /// <summary>
    /// Takes what the user typed. The box breaks its lines with carriage returns, so the text is normalized on the way in
    /// rather than bound two ways: a binding would hand the view model a text that differs from the one it holds when
    /// nothing was changed.
    /// </summary>
    private void OnEditorTextChanged(object sender, TextChangedEventArgs args)
    {
        if (ViewModel is { } model)
        {
            var typed = LineEndings.Normalize(Editor.Text);
            if (typed != model.Content)
            {
                model.Content = typed;
            }
        }
    }

    /// <summary>Control-S saves, as it does in every editor.</summary>
    private void OnSaveInvoked(KeyboardAccelerator sender, KeyboardAcceleratorInvokedEventArgs args)
    {
        args.Handled = true;
        if (ViewModel?.SaveCommand.CanExecute(null) == true)
        {
            ViewModel.SaveCommand.Execute(null);
        }
    }

    /// <summary>Marks the line the daemon refused: it is selected, which is the mark a text box has.</summary>
    private void OnViewModelPropertyChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName != nameof(ConfigurationViewModel.DiagnosticLine) || ViewModel?.DiagnosticLine is not { } line)
        {
            return;
        }

        var lines = Editor.Text.Replace("\r\n", "\r", StringComparison.Ordinal).Split('\r');
        if (line < 1 || line > lines.Length)
        {
            return;
        }

        Editor.SelectionStart = lines.Take(line - 1).Sum(text => text.Length + 1);
        Editor.SelectionLength = lines[line - 1].Length;
    }
}
