using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Dialogs;

/// <summary>What came of importing the files the user picked or dropped.</summary>
internal sealed partial class ImportReportDialog : ContentDialog
{
    public ImportReportDialog(ImportReportViewModel viewModel)
    {
        ViewModel = viewModel;
        InitializeComponent();
    }

    public ImportReportViewModel ViewModel { get; }
}
