using System.ComponentModel;
using System.Windows.Input;
using Microsoft.UI.Input;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Plaitway.AppCore.ViewModels;
using Windows.System;
using Windows.UI.Core;

namespace Plaitway.App.Views;

/// <summary>
/// The sidebar: the profiles, which can be dragged into another order, and under them Diagnostics and Settings. Two lists
/// share one selection, which the model holds; the view only keeps them in step with it.
/// </summary>
internal sealed partial class SidebarView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(SidebarViewModel), typeof(SidebarView), new PropertyMetadata(null, (sender, args) => ((SidebarView)sender).OnViewModelChanged(args)));

    public static readonly DependencyProperty ImportCommandProperty = DependencyProperty.Register(
        nameof(ImportCommand), typeof(ICommand), typeof(SidebarView), new PropertyMetadata(null));

    private bool _isSyncing;

    public SidebarView()
    {
        InitializeComponent();
    }

    public SidebarViewModel? ViewModel
    {
        get => (SidebarViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    /// <summary>What the plus button does.</summary>
    public ICommand? ImportCommand
    {
        get => (ICommand?)GetValue(ImportCommandProperty);
        set => SetValue(ImportCommandProperty, value);
    }

    private void OnViewModelChanged(DependencyPropertyChangedEventArgs args)
    {
        Bindings.Update();
        if (args.OldValue is SidebarViewModel old)
        {
            old.PropertyChanged -= OnViewModelPropertyChanged;
        }

        if (args.NewValue is SidebarViewModel current)
        {
            current.PropertyChanged += OnViewModelPropertyChanged;
            current.Rows.CollectionChanged += (_, _) => DispatcherQueue.TryEnqueue(SyncSelection);
            SyncSelection();
        }
    }

    private void OnViewModelPropertyChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(SidebarViewModel.Selection))
        {
            SyncSelection();
        }
    }

    /// <summary>Shows in the lists what the model has selected.</summary>
    private void SyncSelection()
    {
        if (ViewModel is not { } model)
        {
            return;
        }

        _isSyncing = true;
        try
        {
            ProfileList.SelectedItem = model.Rows.FirstOrDefault(row => row.Id == model.SelectedProfileId);
            FooterList.SelectedItem = model.IsDiagnosticsSelected ? DiagnosticsItem : model.IsSettingsSelected ? SettingsItem : null;
        }
        finally
        {
            _isSyncing = false;
        }
    }

    private void OnProfileSelectionChanged(object sender, SelectionChangedEventArgs args)
    {
        // A row that went away takes its selection with it; the model decides what is selected next.
        if (!_isSyncing && args.AddedItems.OfType<ProfileRowViewModel>().FirstOrDefault() is { } row)
        {
            ViewModel?.SelectProfile(row.Id);
        }
    }

    private void OnFooterSelectionChanged(object sender, SelectionChangedEventArgs args)
    {
        if (_isSyncing || args.AddedItems.FirstOrDefault() is not ListViewItem item)
        {
            return;
        }

        if (ReferenceEquals(item, DiagnosticsItem))
        {
            ViewModel?.SelectDiagnostics();
        }
        else
        {
            ViewModel?.SelectSettings();
        }
    }

    private void OnDragItemsCompleted(ListViewBase sender, DragItemsCompletedEventArgs args) => ViewModel?.ApplyDraggedOrderCommand.Execute(null);

    // Delete removes the profile (after asking); Alt and the arrows move it, which a drag cannot be replaced by for someone without a mouse.
    private void OnProfileListKeyDown(object sender, KeyRoutedEventArgs args)
    {
        if (ProfileList.SelectedItem is not ProfileRowViewModel row || ViewModel is not { } model)
        {
            return;
        }

        var isAlt = InputKeyboardSource.GetKeyStateForCurrentThread(VirtualKey.Menu).HasFlag(CoreVirtualKeyStates.Down);
        switch (args.Key)
        {
            case VirtualKey.Delete:
                model.RequestDeletion(row.Id);
                args.Handled = true;
                break;
            case VirtualKey.Up when isAlt:
                _ = model.MoveUpAsync(row.Id);
                args.Handled = true;
                break;
            case VirtualKey.Down when isAlt:
                _ = model.MoveDownAsync(row.Id);
                args.Handled = true;
                break;
            default:
                break;
        }
    }
}
