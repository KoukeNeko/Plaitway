using Plaitway.AppCore.Dialogs;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>The Service Control Manager, with the state the test sets.</summary>
internal sealed class FakeHelperService : IHelperService
{
    public HelperState State { get; set; } = HelperState.Running;

    public HelperState Query() => State;
}

/// <summary>
/// The consent prompt, answered by the test. It never starts anything: the real launcher would show the user a prompt
/// that no test may answer.
/// </summary>
internal sealed class FakeElevatedLauncher : IElevatedLauncher
{
    private readonly List<string> _calls = [];

    /// <summary>What a run answers with.</summary>
    public ElevatedResult Result { get; set; } = new(ElevatedOutcome.Completed, 0);

    /// <summary>What the command would have done to the service, for the state to change as it does.</summary>
    public Action<string>? Effect { get; set; }

    /// <summary>Thrown by a run, as a program that cannot be started.</summary>
    public Exception? Failure { get; set; }

    /// <summary>The command lines run so far: the program and its arguments.</summary>
    public IReadOnlyList<string> Calls => _calls;

    public Task<ElevatedResult> RunAsync(string executablePath, string arguments, CancellationToken cancellationToken = default)
    {
        _calls.Add($"{executablePath} {arguments}");
        if (Failure is not null)
        {
            throw Failure;
        }

        if (Result.Outcome == ElevatedOutcome.Completed)
        {
            Effect?.Invoke(arguments);
        }

        return Task.FromResult(Result);
    }
}

/// <summary>The startup list, in memory.</summary>
internal sealed class FakeStartup : IStartupRegistration
{
    public bool IsAvailable { get; set; } = true;

    public bool IsEnabled { get; private set; }

    public Exception? Failure { get; set; }

    public void SetEnabled(bool enabled)
    {
        if (Failure is not null)
        {
            throw Failure;
        }

        IsEnabled = enabled;
    }
}

/// <summary>Answers the two questions of Quit and remembers that they were asked.</summary>
internal sealed class FakeQuitPrompts : IQuitPrompts
{
    public bool DiscardEdits { get; set; } = true;

    public QuitAnswer Answer { get; set; } = QuitAnswer.Quit;

    public int DiscardAsked { get; private set; }

    public int QuitAsked { get; private set; }

    public Task<bool> ConfirmDiscardingEditsAsync()
    {
        DiscardAsked++;
        return Task.FromResult(DiscardEdits);
    }

    public Task<QuitAnswer> AskAsync()
    {
        QuitAsked++;
        return Task.FromResult(Answer);
    }
}

/// <summary>The clipboard, in memory.</summary>
internal sealed class FakeClipboard : IClipboard
{
    public string? Text { get; private set; }

    public void SetText(string text) => Text = text;
}

/// <summary>The file dialog, answered by the test.</summary>
internal sealed class FakeFilePicker : IFilePicker
{
    public IReadOnlyList<string> Chosen { get; set; } = [];

    public int Asked { get; private set; }

    public Task<IReadOnlyList<string>> PickProfileFilesAsync()
    {
        Asked++;
        return Task.FromResult(Chosen);
    }
}

/// <summary>
/// The dialogs, answered by the test: a question gets the answer <see cref="Answer"/> gives, and the credential dialog stays
/// until its view model says it is over, as the real one does.
/// </summary>
internal sealed class FakeDialogs : IDialogService
{
    private readonly List<ChoiceRequest> _questions = [];
    private readonly List<CredentialPromptViewModel> _credentials = [];
    private readonly List<ImportReportViewModel> _reports = [];

    /// <summary>What to answer a question with: the index of a button.</summary>
    public Func<ChoiceRequest, int> Answer { get; set; } = request => request.DismissIndex;

    /// <summary>What the user does with a credential dialog before it closes; nothing by default, so that the test can look at it.</summary>
    public Func<CredentialPromptViewModel, Task>? OnCredentials { get; set; }

    public IReadOnlyList<ChoiceRequest> Questions => _questions;

    public IReadOnlyList<CredentialPromptViewModel> Credentials => _credentials;

    public IReadOnlyList<ImportReportViewModel> Reports => _reports;

    public Task<int> AskAsync(ChoiceRequest request)
    {
        _questions.Add(request);
        return Task.FromResult(Answer(request));
    }

    public async Task ShowCredentialsAsync(CredentialPromptViewModel prompt)
    {
        _credentials.Add(prompt);
        if (OnCredentials is not null)
        {
            await OnCredentials(prompt);
        }

        await prompt.Completion;
    }

    public Task ShowImportReportAsync(ImportReportViewModel report)
    {
        _reports.Add(report);
        return Task.CompletedTask;
    }
}
