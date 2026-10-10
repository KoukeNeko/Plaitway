namespace Plaitway.AppCore;

// The language of the app: chosen here, loaded when the app starts, which the app can do again at once.
public sealed partial class AppModel
{
    /// <summary>The host asks to end this run and start another, for a language that was chosen and is not yet in use.</summary>
    public event Action? RestartRequested;

    /// <summary>Keeps the language for the next start; a choice that cannot be kept is reported and the old one stays.</summary>
    /// <param name="tag">A language tag such as <c>zh-TW</c>; null to follow Windows.</param>
    public void SetLanguage(string? tag)
    {
        try
        {
            Language.Choose(tag);
        }
        catch (InvalidOperationException error)
        {
            Report(error, AlertTitle.LanguageNotSaved);
        }
    }

    /// <summary>Asks the host to start the app again.</summary>
    public void RequestRestart() => RestartRequested?.Invoke();
}
