using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Controls;

/// <summary>A list of labels and their values.</summary>
internal sealed partial class DetailRows : UserControl
{
    public static readonly DependencyProperty RowsProperty = DependencyProperty.Register(
        nameof(Rows), typeof(IReadOnlyList<DetailRow>), typeof(DetailRows), new PropertyMetadata(null));

    public DetailRows()
    {
        InitializeComponent();
    }

    /// <summary>What to list.</summary>
    public IReadOnlyList<DetailRow>? Rows
    {
        get => (IReadOnlyList<DetailRow>?)GetValue(RowsProperty);
        set => SetValue(RowsProperty, value);
    }
}
