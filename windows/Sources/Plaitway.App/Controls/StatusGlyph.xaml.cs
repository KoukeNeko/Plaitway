using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.Presentation;

namespace Plaitway.App.Controls;

/// <summary>The glyph of a state in the colour of its tone: the shape says what it is, the colour how much it matters.</summary>
internal sealed partial class StatusGlyph : UserControl
{
    private const double DefaultSize = 16;

    public static readonly DependencyProperty IconProperty = DependencyProperty.Register(
        nameof(Icon), typeof(StatusIcon), typeof(StatusGlyph), new PropertyMetadata(StatusIcon.Idle, OnChanged));

    public static readonly DependencyProperty ToneProperty = DependencyProperty.Register(
        nameof(Tone), typeof(StatusTone), typeof(StatusGlyph), new PropertyMetadata(StatusTone.Neutral, OnChanged));

    public static readonly DependencyProperty GlyphSizeProperty = DependencyProperty.Register(
        nameof(GlyphSize), typeof(double), typeof(StatusGlyph), new PropertyMetadata(DefaultSize, OnChanged));

    public StatusGlyph()
    {
        InitializeComponent();
        Loaded += (_, _) => Refresh();
    }

    /// <summary>What the glyph shows.</summary>
    public StatusIcon Icon
    {
        get => (StatusIcon)GetValue(IconProperty);
        set => SetValue(IconProperty, value);
    }

    /// <summary>How much it matters.</summary>
    public StatusTone Tone
    {
        get => (StatusTone)GetValue(ToneProperty);
        set => SetValue(ToneProperty, value);
    }

    /// <summary>The size of the glyph in device-independent pixels.</summary>
    public double GlyphSize
    {
        get => (double)GetValue(GlyphSizeProperty);
        set => SetValue(GlyphSizeProperty, value);
    }

    private static void OnChanged(DependencyObject sender, DependencyPropertyChangedEventArgs args) => ((StatusGlyph)sender).Refresh();

    private void Refresh()
    {
        Shape.Glyph = StatusGlyphs.Of(Icon);
        Shape.FontSize = GlyphSize;
        VisualStateManager.GoToState(this, Tone.ToString(), useTransitions: false);
    }

}

/// <summary>The characters of Segoe Fluent Icons (Segoe MDL2 Assets on Windows 10) that stand for the icons of the app.</summary>
internal static class StatusGlyphs
{
    /// <summary>The glyph of <paramref name="icon"/>.</summary>
    public static string Of(StatusIcon icon) => icon switch
    {
        StatusIcon.Connected => "",
        StatusIcon.Connecting => "",
        StatusIcon.Reconnecting => "",
        StatusIcon.Disconnecting => "",
        StatusIcon.AwaitingCredentials => "",
        StatusIcon.Failed => "",
        StatusIcon.Unavailable => "",
        StatusIcon.RouteInstalled => "",
        StatusIcon.RoutePending => "",
        StatusIcon.RouteShadowed => "",
        StatusIcon.RouteBlocked => "",
        StatusIcon.RouteFailed => "",
        StatusIcon.RouteUnknown => "",
        _ => "",
    };
}
