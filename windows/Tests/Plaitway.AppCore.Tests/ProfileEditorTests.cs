using Plaitway.AppCore.Editing;
using Plaitway.AppCore.Tests.Support;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>macos/Tests/PlaitwayTests/App/ProfileEditorTests.swift, against the real daemon.</summary>
public sealed class ProfileEditorTests(DaemonBinary binary)
{
    /// <summary>The private key of <see cref="Fixture.WireGuard"/>.</summary>
    private const string PrivateKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

    private static ProfileEditor EditorFor(AppHarness app, string id, ProfileKind kind) => new(id, kind, app.Model.Errors, app.Text);

    private static async Task<(ProfileEditor Editor, string Id)> LoadedWireGuardAsync(AppHarness app)
    {
        var id = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());
        var editor = EditorFor(app, id, ProfileKind.Wireguard);
        await editor.LoadAsync(app.Store);
        return (editor, id);
    }

    private static int LineCount(string text) => text.Split('\n').Length;

    [Fact]
    public async Task ShowsTheStoredTextWithItsSecretsHidden()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);

            Assert.Equal(EditorPhase.Ready, editor.Phase);
            Assert.DoesNotContain(PrivateKey, editor.Text, StringComparison.Ordinal);
            Assert.Contains("‹secret 1›", editor.Text, StringComparison.Ordinal);
            Assert.Contains("Address = 10.6.0.2/32", editor.Text, StringComparison.Ordinal);
            Assert.False(editor.IsDirty, "a text nobody touched is not an edit");
        });
    }

    [Fact]
    public async Task SavesAnEditWithoutLosingTheSecret()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, id) = await LoadedWireGuardAsync(app);
            editor.Text = editor.Text.Replace("10.6.0.2/32", "10.6.0.9/32", StringComparison.Ordinal);
            Assert.True(editor.IsDirty);

            var saved = await editor.SaveAsync(app.Store, reconnect: false, isOn: false);

            Assert.True(saved);
            var stored = await app.Store.GetProfileContentAsync(id);
            Assert.Contains("Address = 10.6.0.9/32", stored, StringComparison.Ordinal);
            Assert.Contains(PrivateKey, stored, StringComparison.Ordinal);
            Assert.DoesNotContain(PrivateKey, editor.Text, StringComparison.Ordinal);
            Assert.False(editor.IsDirty);
            Assert.Null(editor.Diagnostic);
            Assert.False(editor.RunsOldText, "the profile is not on");
        });
    }

    [Fact]
    public async Task ASavedProfileThatIsOnKeepsTheOldTextUntilItReconnects()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);
            editor.Text += "# a note\n";
            Assert.True(await editor.SaveAsync(app.Store, reconnect: false, isOn: true));
            Assert.True(editor.RunsOldText);

            editor.Text += "# another\n";
            Assert.True(await editor.SaveAsync(app.Store, reconnect: true, isOn: true));
            Assert.False(editor.RunsOldText, "a restart applies the text at once");
        });
    }

    [Fact]
    public async Task ShowsTheSecretsOnRequestAndHidesThemAgainWithTheEditsMadeMeanwhile()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);
            editor.ToggleSecrets();
            Assert.True(editor.ShowsSecrets);
            Assert.Contains(PrivateKey, editor.Text, StringComparison.Ordinal);
            Assert.False(editor.IsDirty);

            editor.Text = editor.Text.Replace("10.6.0.2/32", "10.6.0.7/32", StringComparison.Ordinal);
            Assert.True(editor.IsDirty);

            editor.ToggleSecrets();
            Assert.False(editor.ShowsSecrets);
            Assert.DoesNotContain(PrivateKey, editor.Text, StringComparison.Ordinal);
            Assert.Contains("10.6.0.7/32", editor.Text, StringComparison.Ordinal);
            Assert.True(editor.IsDirty, "the edit survives hiding the secrets");
        });
    }

    [Fact]
    public async Task ASecretTypedOverIsStoredAsTheNewSecret()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, id) = await LoadedWireGuardAsync(app);
            const string replacement = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=";
            editor.Text = editor.Text.Replace("‹secret 1›", replacement, StringComparison.Ordinal);

            Assert.True(await editor.SaveAsync(app.Store, reconnect: false, isOn: false));

            var stored = await app.Store.GetProfileContentAsync(id);
            Assert.Contains($"PrivateKey = {replacement}", stored, StringComparison.Ordinal);
            Assert.DoesNotContain(PrivateKey, stored, StringComparison.Ordinal);
        });
    }

    [Fact]
    public async Task ARefusedTextStaysInTheEditorAndIsMarkedAtItsLine()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn", Fixture.OpenVpn());
            var editor = EditorFor(app, id, ProfileKind.Openvpn);
            await editor.LoadAsync(app.Store);
            var before = await app.Store.GetProfileContentAsync(id);

            // The fake daemon refuses a text with this marker, naming the line it stands on.
            editor.Text += "# fake: reject\n";
            var markerLine = LineCount(editor.Text) - 1;

            var saved = await editor.SaveAsync(app.Store, reconnect: false, isOn: false);

            Assert.False(saved);
            Assert.Equal(markerLine, editor.Diagnostic?.Line);
            Assert.False(string.IsNullOrEmpty(editor.Diagnostic?.Message));
            Assert.EndsWith("# fake: reject\n", editor.Text, StringComparison.Ordinal);
            Assert.True(editor.IsDirty);
            Assert.Equal(before, await app.Store.GetProfileContentAsync(id));
        });
    }

    [Fact]
    public async Task ARefusalIsMarkedAtTheLineThePersonSees()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);

            // The key is one line in the editor and one in the text, so a refusal of the line after it names the same
            // line in both; the marker makes the fake daemon refuse.
            editor.Text = editor.Text.Replace("DNS = 10.6.0.1", "# fake: reject", StringComparison.Ordinal);
            var line = Array.IndexOf(editor.Text.Split('\n'), "# fake: reject") + 1;

            Assert.False(await editor.SaveAsync(app.Store, reconnect: false, isOn: false));
            Assert.Equal(line, editor.Diagnostic?.Line);
        });
    }

    [Fact]
    public async Task APlaceholderThatStandsTwiceIsNotSent()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, id) = await LoadedWireGuardAsync(app);
            var before = await app.Store.GetProfileContentAsync(id);
            editor.Text += "# ‹secret 1›\n";

            var saved = await editor.SaveAsync(app.Store, reconnect: false, isOn: false);

            Assert.False(saved);
            Assert.Equal(LineCount(editor.Text) - 1, editor.Diagnostic?.Line);
            Assert.Equal(before, await app.Store.GetProfileContentAsync(id));

            // It cannot be shown either: a secret is in one place.
            editor.ToggleSecrets();
            Assert.False(editor.ShowsSecrets);
        });
    }

    [Fact]
    public async Task RevertGoesBackToTheStoredText()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);
            var original = editor.Text;
            editor.Text += "garbage\n";
            Assert.True(editor.IsDirty);

            editor.Revert();

            Assert.Equal(original, editor.Text);
            Assert.False(editor.IsDirty);
            Assert.Null(editor.Diagnostic);
        });
    }

    [Fact]
    public async Task ALoadDoesNotThrowAwayEdits()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);
            editor.Text += "# unsaved\n";

            await editor.LoadAsync(app.Store);

            Assert.EndsWith("# unsaved\n", editor.Text, StringComparison.Ordinal);
        });
    }

    [Fact]
    public async Task AProfileThatIsGoneCannotBeRead()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var editor = EditorFor(app, "nope", ProfileKind.Openvpn);

            await editor.LoadAsync(app.Store);

            Assert.Equal(EditorPhase.Unavailable, editor.Phase);
            Assert.Equal(app.Text.ProfileNotFound, editor.UnavailableMessage);
            Assert.False(editor.IsDirty);
        });
    }

    [Fact]
    public async Task HidesTheSecretsAgainWhenThePageIsLeft()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);
            editor.ToggleSecrets();
            editor.Text = editor.Text.Replace("10.6.0.2/32", "10.6.0.8/32", StringComparison.Ordinal);

            editor.HideSecrets();

            Assert.False(editor.ShowsSecrets);
            Assert.DoesNotContain(PrivateKey, editor.Text, StringComparison.Ordinal);
            Assert.Contains("10.6.0.8/32", editor.Text, StringComparison.Ordinal);
            Assert.True(editor.IsDirty);

            // Nothing to hide: nothing happens.
            var hidden = editor.Text;
            editor.HideSecrets();
            Assert.Equal(hidden, editor.Text);
        });
    }

    [Fact]
    public async Task ShowingOrHidingTheSecretsClearsAMarkThatNamedALineOfTheOtherView()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn", Fixture.OpenVpn());
            var editor = EditorFor(app, id, ProfileKind.Openvpn);
            await editor.LoadAsync(app.Store);
            editor.Text += "# fake: reject\n";
            Assert.False(await editor.SaveAsync(app.Store, reconnect: false, isOn: false));
            Assert.NotNull(editor.Diagnostic);

            editor.ToggleSecrets();

            Assert.Null(editor.Diagnostic);
        });
    }

    [Fact]
    public async Task AProfileThatConnectsAgainRunsTheSavedText()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);
            editor.Text += "# a note\n";
            Assert.True(await editor.SaveAsync(app.Store, reconnect: false, isOn: true));
            Assert.True(editor.RunsOldText);

            editor.NoteRestart();

            Assert.False(editor.RunsOldText);
        });
    }

    [Fact]
    public async Task TheModelKnowsOfEditsThatAreNotSaved()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());
            var profile = app.Store.Find(id)!;
            Assert.False(app.Model.HasUnsavedEdits);

            var editor = app.Model.EditorFor(profile);
            await editor.LoadAsync(app.Store);
            Assert.False(app.Model.HasUnsavedEdits, "reading the text is not an edit");

            editor.Text += "# unsaved\n";
            Assert.True(app.Model.HasUnsavedEdits);

            editor.Revert();
            Assert.False(app.Model.HasUnsavedEdits);
        });
    }

    [Fact]
    public async Task ChangingTheTextTellsTheViewThatTheEditorIsDirty()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);
            var changed = new List<string?>();
            editor.PropertyChanged += (_, args) => changed.Add(args.PropertyName);

            editor.Text += "# a note\n";

            Assert.Contains(nameof(ProfileEditor.IsDirty), changed);
        });
    }

    [Fact]
    public async Task AFileWithWindowsLineEndingsIsNotDirtyUntilItIsChanged()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var windowsText = LineEndings.Restore(System.Text.Encoding.UTF8.GetString(Fixture.WireGuard()).ReplaceLineEndings("\n"), usesCarriageReturn: true);
            var id = await app.ImportFixtureAsync("home.conf", System.Text.Encoding.UTF8.GetBytes(windowsText));
            var editor = EditorFor(app, id, ProfileKind.Wireguard);

            await editor.LoadAsync(app.Store);

            Assert.DoesNotContain('\r', editor.Text);
            Assert.False(editor.IsDirty);
        });
    }

    [Fact]
    public async Task ATextBoxsLineBreaksAreTheSameTextAsTheStoredOnes()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var (editor, _) = await LoadedWireGuardAsync(app);

            // The box hands back what it was given, with its breaks turned into carriage returns.
            editor.Text = editor.Text.Replace("\n", "\r", StringComparison.Ordinal);

            Assert.DoesNotContain('\r', editor.Text);
            Assert.False(editor.IsDirty, "the same text with another kind of break is not an edit");
        });
    }

    [Fact]
    public async Task ASavedFileKeepsTheLineEndingsItHad()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var windowsText = LineEndings.Restore(System.Text.Encoding.UTF8.GetString(Fixture.WireGuard()).ReplaceLineEndings("\n"), usesCarriageReturn: true);
            var id = await app.ImportFixtureAsync("home.conf", System.Text.Encoding.UTF8.GetBytes(windowsText));
            var editor = EditorFor(app, id, ProfileKind.Wireguard);
            await editor.LoadAsync(app.Store);

            editor.Text = editor.Text.Replace("10.6.0.2/32", "10.6.0.9/32", StringComparison.Ordinal).Replace("\n", "\r", StringComparison.Ordinal);
            Assert.True(await editor.SaveAsync(app.Store, reconnect: false, isOn: false));

            var stored = await app.Store.GetProfileContentAsync(id);
            Assert.Contains("10.6.0.9/32", stored, StringComparison.Ordinal);
            Assert.DoesNotContain("\n", stored.Replace("\r\n", string.Empty, StringComparison.Ordinal), StringComparison.Ordinal);
            Assert.False(editor.IsDirty);
        });
    }
}
