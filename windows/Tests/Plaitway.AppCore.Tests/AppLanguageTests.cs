using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.ViewModels;
using Plaitway.Client.Tests.Support;

namespace Plaitway.AppCore.Tests;

/// <summary>The language of the app: what is offered, what is kept, and when a restart is needed.</summary>
public sealed class AppLanguageTests
{
    [Fact]
    public void WindowsLanguageIsOfferedFirstAndTheOthersAreNamedInTheirOwnWords()
    {
        var language = new AppLanguage(new FakeLanguagePreference(), TestText.Zh);

        Assert.Equal(
            [new LanguageChoice(null, TestText.Zh.SystemDefault), new LanguageChoice("en-US", "English"), new LanguageChoice("zh-TW", "繁體中文")],
            language.Choices);
    }

    [Fact]
    public void AChoiceIsKeptForTheNextStartAndTheRunInProgressKeepsItsLanguage()
    {
        var preference = new FakeLanguagePreference();
        var language = new AppLanguage(preference, TestText.En);
        Assert.False(language.IsRestartNeeded);

        language.Choose("zh-TW");

        Assert.Equal("zh-TW", preference.Language);
        Assert.Equal("zh-TW", language.Chosen);
        Assert.Null(language.ChosenAtStart);
        Assert.True(language.IsRestartNeeded);

        language.Choose(null);

        Assert.Null(preference.Language);
        Assert.False(language.IsRestartNeeded);
    }

    [Fact]
    public void TheChoiceOfTheLastRunIsTheLanguageOfThisOne()
    {
        var language = new AppLanguage(new FakeLanguagePreference { Language = "zh-TW" }, TestText.Zh);

        Assert.Equal("zh-TW", language.Chosen);
        Assert.False(language.IsRestartNeeded);
    }

    [Fact]
    public void ALanguageTheAppHasNoStringsForIsNotAChoice()
    {
        var preference = new FakeLanguagePreference { Language = "fr-FR" };
        var language = new AppLanguage(preference, TestText.En);

        Assert.Null(language.Chosen);

        language.Choose("de-DE");

        Assert.Null(preference.Language);
        Assert.Null(language.Chosen);
    }

    [Fact]
    public void ATagIsKeptAsTheAppWritesIt()
    {
        var language = new AppLanguage(new FakeLanguagePreference { Language = "ZH-tw" }, TestText.En);

        Assert.Equal("zh-TW", language.Chosen);
        Assert.False(language.IsRestartNeeded);
    }

    [Fact]
    public void AChoiceThatCannotBeKeptStaysWhatItWas()
    {
        var preference = new FakeLanguagePreference { Language = "en-US" };
        var language = new AppLanguage(preference, TestText.En);
        preference.Failure = new InvalidOperationException("the folder is read-only");

        Assert.Throws<InvalidOperationException>(() => language.Choose("zh-TW"));

        Assert.Equal("en-US", language.Chosen);
        Assert.False(language.IsRestartNeeded);
    }

    [Fact]
    public void TheTagsAreTheLanguagesThatTheStringsAreBuiltFor()
    {
        Assert.Equal(TestText.English, AppLanguage.English);
        Assert.Equal(TestText.TraditionalChinese, AppLanguage.TraditionalChinese);
    }
}

/// <summary>The file that keeps the language.</summary>
public sealed class FileLanguagePreferenceTests : IDisposable
{
    private readonly string _folder = Path.Combine(Path.GetTempPath(), "plaitway-language-" + Guid.NewGuid().ToString("N"));

    private string FilePath => Path.Combine(_folder, "settings.json");

    public void Dispose()
    {
        if (Directory.Exists(_folder))
        {
            Directory.Delete(_folder, recursive: true);
        }
    }

    [Fact]
    public void AChoiceComesBackAtTheNextStartAndFollowingWindowsComesBackToo()
    {
        new FileLanguagePreference(FilePath).Save("zh-TW");
        Assert.Equal("zh-TW", new FileLanguagePreference(FilePath).Language);

        new FileLanguagePreference(FilePath).Save(null);
        Assert.Null(new FileLanguagePreference(FilePath).Language);
    }

    [Fact]
    public void WithoutAFileTheAppFollowsWindows()
    {
        Assert.Null(new FileLanguagePreference(FilePath).Language);
    }

    [Theory]
    [InlineData("")]
    [InlineData("not json")]
    [InlineData("[1, 2]")]
    public void AFileThatIsNotOursIsNotAChoice(string content)
    {
        Directory.CreateDirectory(_folder);
        File.WriteAllText(FilePath, content);

        Assert.Null(new FileLanguagePreference(FilePath).Language);
    }

    [Fact]
    public void AFolderThatCannotBeWrittenToIsReportedAsAFailureToKeepTheChoice()
    {
        // A file where the folder should be.
        File.WriteAllText(Path.Combine(Path.GetTempPath(), Path.GetFileName(_folder)), "in the way");
        try
        {
            Assert.Throws<InvalidOperationException>(() => new FileLanguagePreference(Path.Combine(_folder, "settings.json")).Save("zh-TW"));
        }
        finally
        {
            File.Delete(_folder);
        }
    }
}

/// <summary>The settings page and the restart, on the model.</summary>
public sealed class AppLanguageSettingsTests(DaemonBinary binary)
{
    [Fact]
    public async Task PickingALanguageOnTheSettingsPageKeepsItAndOffersARestart()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(() =>
        {
            using var settings = new AppSettingsViewModel(app.Model);
            Assert.Null(settings.SelectedLanguage!.Tag);
            Assert.False(settings.IsRestartNeeded);

            settings.SelectedLanguage = settings.Languages.First(choice => choice.Tag == "zh-TW");

            Assert.Equal("zh-TW", app.LanguagePreference.Language);
            Assert.True(settings.IsRestartNeeded);

            settings.SelectedLanguage = settings.Languages.First(choice => choice.Tag is null);

            Assert.Null(app.LanguagePreference.Language);
            Assert.False(settings.IsRestartNeeded);
            return Task.CompletedTask;
        });
    }

    [Fact]
    public async Task ALanguageThatCannotBeKeptIsReportedAndThePickerGoesBack()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(() =>
        {
            using var settings = new AppSettingsViewModel(app.Model);
            app.LanguagePreference.Failure = new InvalidOperationException("the folder is read-only");

            settings.SelectedLanguage = settings.Languages.First(choice => choice.Tag == "zh-TW");

            Assert.Equal(AppAlert.TitleText(AlertTitle.LanguageNotSaved, app.Text), app.Model.Alert?.Title);
            Assert.Null(settings.SelectedLanguage!.Tag);
            Assert.False(settings.IsRestartNeeded);
            return Task.CompletedTask;
        });
    }

    [Fact]
    public async Task TheRestartButtonAsksTheHostToStartTheAppAgain()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(() =>
        {
            using var settings = new AppSettingsViewModel(app.Model);
            var requests = 0;
            app.Model.RestartRequested += () => requests++;

            settings.RestartCommand.Execute(null);

            Assert.Equal(1, requests);
            return Task.CompletedTask;
        });
    }

    [Fact]
    public async Task ARestartAsksOnlyBeforeTextThatWasNotSavedIsLost()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var prompts = new FakeQuitPrompts { DiscardEdits = false };
            Assert.True(await app.Model.RequestRestartAsync(prompts));
            Assert.Equal(0, prompts.DiscardAsked);

            var id = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());
            var editor = app.Model.EditorFor(app.Store.Find(id)!);
            await editor.LoadAsync(app.Store);
            editor.Text += "# unsaved\n";

            Assert.False(await app.Model.RequestRestartAsync(prompts));
            Assert.Equal(1, prompts.DiscardAsked);

            prompts.DiscardEdits = true;
            Assert.True(await app.Model.RequestRestartAsync(prompts));
            Assert.Equal(0, prompts.QuitAsked);
        });
    }
}
