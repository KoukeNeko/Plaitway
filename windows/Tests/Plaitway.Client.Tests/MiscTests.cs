using Plaitway.Client.Transport;
using Plaitway.V1;

namespace Plaitway.Client.Tests;

public sealed class BackoffPolicyTests
{
    [Fact]
    public void GrowsByTheMultiplierToTheMaximumWithoutJitter()
    {
        var policy = new BackoffPolicy(TimeSpan.FromMilliseconds(200), TimeSpan.FromSeconds(5), 1.6, 0.2);

        var delays = Enumerable.Range(1, 12).Select(failures => policy.Delay(failures, 0.5).TotalMilliseconds).ToArray();

        Assert.Equal(200, delays[0], 3);
        Assert.Equal(320, delays[1], 3);
        Assert.Equal(512, delays[2], 3);
        Assert.Equal(5000, delays[^1], 3);
        Assert.True(delays.Zip(delays.Skip(1)).All(pair => pair.Second >= pair.First));
    }

    [Theory]
    [InlineData(0.0, 160)]
    [InlineData(0.5, 200)]
    [InlineData(0.999999, 240)]
    public void JitterMovesTheDelayByAtMostItsFractionEitherWay(double random, double expectedMilliseconds)
    {
        var policy = BackoffPolicy.Default;
        Assert.Equal(expectedMilliseconds, policy.Delay(1, random).TotalMilliseconds, 0.01);
    }
}

public sealed class ProfileSetTests
{
    private static Profile P(string id, int priority, ProfileState state = ProfileState.Disconnected) =>
        new() { Id = id, State = state, Settings = new ProfileSettings { Priority = priority } };

    [Fact]
    public void OrdersASnapshotByPriority()
    {
        var set = new ProfileSet();
        set.Apply(new ProfilesSnapshot([P("c", 3), P("a", 1), P("b", 2)]));
        Assert.Equal(["a", "b", "c"], set.Profiles.Select(profile => profile.Id));
    }

    [Fact]
    public void ProfilesWithEqualPriorityKeepTheirPlaceInTheMiddleOfAReorder()
    {
        var set = new ProfileSet();
        set.Apply(new ProfilesSnapshot([P("a", 1), P("b", 2), P("c", 3)]));

        // The daemon reports a reorder one profile at a time: c gets priority 1 while a still has it.
        set.Apply(new ProfileChanged(P("c", 1)));
        Assert.Equal(["a", "c", "b"], set.Profiles.Select(profile => profile.Id));
        set.Apply(new ProfileChanged(P("a", 2)));
        set.Apply(new ProfileChanged(P("b", 3)));
        Assert.Equal(["c", "a", "b"], set.Profiles.Select(profile => profile.Id));
    }

    [Fact]
    public void AddsChangesAndRemoves()
    {
        var set = new ProfileSet();
        set.Apply(new ProfilesSnapshot([P("a", 1)]));
        set.Apply(new ProfileChanged(P("b", 2)));
        set.Apply(new ProfileChanged(P("a", 1, ProfileState.Connected)));
        Assert.Equal(ProfileState.Connected, set.Find("a")?.State);
        Assert.Equal(2, set.Profiles.Count);

        set.Apply(new ProfileRemoved("a"));
        Assert.Null(set.Find("a"));
        Assert.Equal(["b"], set.Profiles.Select(profile => profile.Id));
    }

    [Fact]
    public void KeepsTheLastProfilesWhileTheDaemonIsUnavailable()
    {
        var set = new ProfileSet();
        set.Apply(new ProfilesSnapshot([P("a", 1)]));

        set.Apply(new DaemonUnavailable(new IOException("gone")));

        Assert.Single(set.Profiles);
    }
}

public sealed class DaemonLocationTests
{
    private static Dictionary<string, string?> Environment(string? socket) =>
        socket is null ? [] : new() { [DaemonLocation.OverrideVariable] = socket };

    [Fact]
    public void UsesTheProductionPipeWithoutAnOverride()
    {
        Assert.Equal(@"\\.\pipe\plaitway", DaemonLocation.Pipe(Environment(null)).FullPath);
        Assert.Equal(@"\\.\pipe\plaitway", DaemonLocation.Pipe(Environment(string.Empty)).FullPath);
        Assert.Null(DaemonLocation.Override(Environment(null)));
    }

    [Fact]
    public void ParsesTheOverrideWhateverTheBuild()
    {
        Assert.Equal(@"\\.\pipe\dev", DaemonLocation.ParseOverride(Environment(@"\\.\pipe\dev"))?.FullPath);
        Assert.Null(DaemonLocation.ParseOverride(Environment(string.Empty)));
        Assert.Null(DaemonLocation.ParseOverride(Environment(null)));
    }

    [Theory]
    [InlineData(@"C:\Users\x\plaitway.sock")]
    [InlineData("/tmp/x.sock")]
    [InlineData("plaitway")]
    [InlineData(@"\\server\pipe\plaitway")]
    public void RefusesAnOverrideThatIsNotALocalNamedPipe(string value) =>
        Assert.Throws<InvalidPipePathException>(() => DaemonLocation.ParseOverride(Environment(value)));

    /// <summary>The app hands saved credentials to whatever listens on the pipe it connects to, so a release build ignores the variable.</summary>
    [Fact]
    public void TheVariableIsFollowedInADebugBuildOnly()
    {
        var environment = Environment(@"\\.\pipe\dev");
        var enabled = DaemonLocation.OverrideEnabled;
        if (enabled)
        {
            Assert.Equal(@"\\.\pipe\dev", DaemonLocation.Pipe(environment).FullPath);
            Assert.Equal(@"\\.\pipe\dev", DaemonLocation.Override(environment)?.FullPath);
        }
        else
        {
            Assert.Equal(DaemonLocation.ProductionPipe, DaemonLocation.Pipe(environment));
            Assert.Null(DaemonLocation.Override(environment));
        }
    }

    [Fact]
    public void ReadsTheEnvironmentOfTheProcess()
    {
        var environment = DaemonLocation.CurrentEnvironment();
        Assert.True(environment.ContainsKey("PATH"));
    }
}
