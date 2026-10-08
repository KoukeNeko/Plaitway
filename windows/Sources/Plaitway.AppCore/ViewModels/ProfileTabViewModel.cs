using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>A page that does something while it is on screen: it starts when it appears and ends when it is left.</summary>
public interface IPageLifecycle
{
    /// <summary>The page appeared.</summary>
    void Activate();

    /// <summary>The page was left; whatever it started in the background ends.</summary>
    void Deactivate();
}

/// <summary>
/// What every tab of a profile shares: the model, the profile it is about, and a <see cref="Refresh"/> that runs
/// whenever the daemon reports the profiles, so that a tab says what the daemon says and nothing else.
/// </summary>
/// <param name="model">The app.</param>
/// <param name="profileId">The profile the tab is about.</param>
public abstract class ProfileTabViewModel(AppModel model, string profileId) : ObservableObject, IDisposable
{
    private bool _isSubscribed;

    /// <summary>The app.</summary>
    protected AppModel Model { get; } = model;

    /// <summary>The strings.</summary>
    public UiText Text => Model.Text;

    /// <summary>The profile the tab is about.</summary>
    public string ProfileId { get; } = profileId;

    /// <summary>The profile as the daemon last reported it; null once it is gone.</summary>
    protected Profile? Profile => Model.Store.Find(ProfileId);

    /// <inheritdoc />
    public void Dispose()
    {
        Model.Store.PropertyChanged -= OnStoreChanged;
        _isSubscribed = false;
        Release();
        GC.SuppressFinalize(this);
    }

    /// <summary>Starts following the daemon and shows what it says now. Call it once, when the tab is made.</summary>
    protected void Follow()
    {
        if (!_isSubscribed)
        {
            Model.Store.PropertyChanged += OnStoreChanged;
            _isSubscribed = true;
        }

        if (Profile is { } profile)
        {
            Refresh(profile);
        }
    }

    /// <summary>Takes what the daemon says about the profile.</summary>
    protected abstract void Refresh(Profile profile);

    /// <summary>Lets go of what the tab holds besides its subscription.</summary>
    protected virtual void Release()
    {
    }

    private void OnStoreChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(ProfileStore.Profiles) && Profile is { } profile)
        {
            Refresh(profile);
        }
    }
}
