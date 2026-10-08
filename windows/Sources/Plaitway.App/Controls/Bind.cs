using Microsoft.UI.Xaml;

namespace Plaitway.App.Controls;

/// <summary>Small functions for <c>x:Bind</c> that XAML cannot say: a value turned into something a property takes.</summary>
internal static class Bind
{
    /// <summary>Shown when <paramref name="value"/> holds.</summary>
    public static Visibility Visible(bool value) => value ? Visibility.Visible : Visibility.Collapsed;

    /// <summary>Shown when <paramref name="value"/> does not hold.</summary>
    public static Visibility Hidden(bool value) => value ? Visibility.Collapsed : Visibility.Visible;

    /// <summary>Shown when there is some text.</summary>
    public static Visibility VisibleIfText(string? value) => string.IsNullOrEmpty(value) ? Visibility.Collapsed : Visibility.Visible;

    /// <summary>Shown when the object exists.</summary>
    public static Visibility VisibleIfSet(object? value) => value is null ? Visibility.Collapsed : Visibility.Visible;

    /// <summary>Two names as one: what a screen reader calls a list that holds both.</summary>
    public static string Join(string first, string second) => $"{first}, {second}";

    /// <summary>The opposite of <paramref name="value"/>.</summary>
    public static bool Not(bool value) => !value;

    /// <summary>Shown when the count is more than none; the count of a list the view model replaces whenever it changes.</summary>
    public static Visibility VisibleIfAny(int count) => count > 0 ? Visibility.Visible : Visibility.Collapsed;

    /// <summary>Shown when the count is none.</summary>
    public static Visibility VisibleIfNone(int count) => count > 0 ? Visibility.Collapsed : Visibility.Visible;
}
