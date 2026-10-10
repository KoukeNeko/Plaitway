namespace Plaitway.AppCore.Platform;

/// <summary>What Quit asks while profiles are switched on: the helper, not the app, keeps them connected.</summary>
public enum QuitAnswer
{
    /// <summary>Quit and leave the profiles connected.</summary>
    Quit,

    /// <summary>Switch every profile off, then quit.</summary>
    DisconnectAndQuit,

    /// <summary>Stay.</summary>
    Cancel,
}

/// <summary>The two questions Quit can ask. The view shows them as dialogs; the tests answer them.</summary>
public interface IQuitPrompts
{
    /// <summary>Edits of profile text that were not saved go with the app. True when the person quits anyway.</summary>
    Task<bool> ConfirmDiscardingEditsAsync();

    /// <summary>Profiles are switched on: what should quitting do to them.</summary>
    Task<QuitAnswer> AskAsync();
}

/// <summary>What the app does about Quit.</summary>
/// <param name="CanQuit">The app may end now.</param>
/// <param name="ShowWindow">The window should come forward: there is something to look at.</param>
public readonly record struct QuitDecision(bool CanQuit, bool ShowWindow);

/// <summary>The language the person chose for the app, kept for the next start.</summary>
public interface ILanguagePreference
{
    /// <summary>The tag chosen, such as <c>zh-TW</c>; null while the app follows the language of Windows.</summary>
    string? Language { get; }

    /// <summary>Keeps the choice; null to follow the language of Windows.</summary>
    /// <exception cref="InvalidOperationException">The choice cannot be kept.</exception>
    void Save(string? language);
}

/// <summary>Starting with Windows, which is an entry of the user's own startup list.</summary>
public interface IStartupRegistration
{
    /// <summary>The app can register itself: it runs from a place that stays.</summary>
    bool IsAvailable { get; }

    /// <summary>The app is in the startup list now.</summary>
    bool IsEnabled { get; }

    /// <summary>Adds the app to the startup list, or removes it.</summary>
    /// <exception cref="InvalidOperationException">The list cannot be changed.</exception>
    void SetEnabled(bool enabled);
}
