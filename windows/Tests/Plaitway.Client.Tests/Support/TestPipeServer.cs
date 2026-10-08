using System.IO.Pipes;
using System.Security.AccessControl;
using System.Security.Principal;
using Plaitway.Client.Transport;

namespace Plaitway.Client.Tests.Support;

/// <summary>
/// A pipe served by this test process with the access list of the daemon's pipe
/// (<c>pipeSecurityDescriptor</c> in transport_windows.go) and an owner the test chooses, which is what
/// the client checks. It counts the bytes any client sends, to show that a refused server got none.
/// </summary>
public sealed class TestPipeServer : IAsyncDisposable
{
    private const int MaxInstances = 8;

    private readonly string _sddl;
    private readonly CancellationTokenSource _stop = new();
    private readonly List<Task> _connections = [];
    private readonly Task _accepting;
    private long _received;
    private int _accepted;
    private int _hungUp;

    private TestPipeServer(SecurityIdentifier owner, string? accessList)
    {
        PipeName = $"plaitway-cs-test-server-{Environment.ProcessId}-{Guid.NewGuid().ToString("N")[..12]}";
        var access = accessList
            ?? $"D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;{CurrentUser().Value})(A;;0x{(int)PipeDialer.ClientAccess:x};;;IU)";
        _sddl = $"O:{owner.Value}{access}";
        _accepting = Task.Run(AcceptLoopAsync);
    }

    /// <summary>The pipe's name, without the namespace.</summary>
    public string PipeName { get; }

    /// <summary>The pipe as the client takes it.</summary>
    public PipePath Pipe => PipePath.Parse(PipePath.Namespace + PipeName);

    /// <summary>How many bytes clients have sent.</summary>
    public long BytesReceived => Interlocked.Read(ref _received);

    /// <summary>How many clients have connected.</summary>
    public int Connections => Volatile.Read(ref _accepted);

    /// <summary>How many clients have hung up: the server read the end of the stream, or found the connection broken.</summary>
    public int HangUps => Volatile.Read(ref _hungUp);

    /// <summary>The user the test runs as.</summary>
    public static SecurityIdentifier CurrentUser()
    {
        using var identity = WindowsIdentity.GetCurrent();
        return identity.User!;
    }

    /// <summary>Serves a pipe whose owner is <paramref name="owner"/>; it has to be a SID that the creator may own.</summary>
    public static TestPipeServer OwnedBy(SecurityIdentifier owner) => new(owner, accessList: null);

    /// <summary>Serves a pipe whose access list refuses the user the test runs as.</summary>
    public static TestPipeServer Refusing()
    {
        var user = CurrentUser();
        return new TestPipeServer(user, $"D:P(D;;GA;;;{user.Value})(A;;GA;;;SY)");
    }

    /// <summary>Waits until a client has connected.</summary>
    public Task WaitForConnectionAsync() => DaemonSession.WaitUntilAsync("a client to connect", () => Connections > 0, TimeSpan.FromSeconds(10));

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        await _stop.CancelAsync().ConfigureAwait(false);
        try
        {
            await _accepting.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // The accept loop ends by cancellation: that is how it is stopped.
        }

        await Task.WhenAll(_connections).ConfigureAwait(false);
        _stop.Dispose();
    }

    private NamedPipeServerStream CreateInstance()
    {
        var security = new PipeSecurity();
        security.SetSecurityDescriptorSddlForm(_sddl);
        return NamedPipeServerStreamAcl.Create(
            PipeName, PipeDirection.InOut, MaxInstances, PipeTransmissionMode.Byte, PipeOptions.Asynchronous, 0, 0, security);
    }

    private async Task AcceptLoopAsync()
    {
        while (!_stop.IsCancellationRequested)
        {
            var instance = CreateInstance();
            try
            {
                await instance.WaitForConnectionAsync(_stop.Token).ConfigureAwait(false);
            }
            catch
            {
                await instance.DisposeAsync().ConfigureAwait(false);
                throw;
            }

            Interlocked.Increment(ref _accepted);
            lock (_connections)
            {
                _connections.Add(CountAsync(instance));
            }
        }
    }

    /// <summary>Reads until the client hangs up, counting what it sent.</summary>
    private async Task CountAsync(NamedPipeServerStream instance)
    {
        await using (instance.ConfigureAwait(false))
        {
            var buffer = new byte[4096];
            try
            {
                int read;
                while ((read = await instance.ReadAsync(buffer, _stop.Token).ConfigureAwait(false)) > 0)
                {
                    Interlocked.Add(ref _received, read);
                }

                Interlocked.Increment(ref _hungUp);
            }
            catch (IOException)
            {
                // The client went away abruptly: the same end as a hang-up.
                Interlocked.Increment(ref _hungUp);
            }
            catch (OperationCanceledException)
            {
                // The server is being stopped: nobody hung up.
            }
        }
    }
}
