using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.Presentation;

namespace Plaitway.App.Controls;

/// <summary>A short word in the colour family of a tone.</summary>
internal sealed partial class ToneText : UserControl
{
    public static readonly DependencyProperty TextProperty = DependencyProperty.Register(
        nameof(Text), typeof(string), typeof(ToneText), new PropertyMetadata(string.Empty, OnChanged));

    public static readonly DependencyProperty ToneProperty = DependencyProperty.Register(
        nameof(Tone), typeof(StatusTone), typeof(ToneText), new PropertyMetadata(StatusTone.Neutral, OnChanged));

    public ToneText()
    {
        InitializeComponent();
        Loaded += (_, _) => Refresh();
    }

    /// <summary>The word.</summary>
    public string Text
    {
        get => (string)GetValue(TextProperty);
        set => SetValue(TextProperty, value);
    }

    /// <summary>How much it matters.</summary>
    public StatusTone Tone
    {
        get => (StatusTone)GetValue(ToneProperty);
        set => SetValue(ToneProperty, value);
    }

    private static void OnChanged(DependencyObject sender, DependencyPropertyChangedEventArgs args) => ((ToneText)sender).Refresh();

    private void Refresh()
    {
        Label.Text = Text;
        VisualStateManager.GoToState(this, Tone.ToString(), useTransitions: false);
    }
}
