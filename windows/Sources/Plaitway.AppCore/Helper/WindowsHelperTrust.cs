using System.Runtime.InteropServices;
using System.Security.AccessControl;
using System.Security.Cryptography.X509Certificates;
using System.Security.Principal;

namespace Plaitway.AppCore.Helper;

/// <summary>
/// <see cref="IHelperTrust"/> over Windows. The folder rule is the one the daemon applies to its own location
/// (<c>CheckAdminOnlyPath</c> in <c>internal/fsperm</c>): every object from the volume root to the file is owned by SYSTEM,
/// Administrators or TrustedInstaller, is no link, and has no entry that lets another account replace it; the file and its
/// folder also must not let another account change their contents, because a library next to a program is loaded by it. The
/// signature is checked with WinVerifyTrust and the publisher is the subject of the signing certificate.
/// </summary>
public sealed partial class WindowsHelperTrust : IHelperTrust
{
    private const string TrustedInstallerSid = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464";
    private const string CreatorOwnerSid = "S-1-3-0";

    private const FileSystemRights RightsToReplaceAnObject =
        FileSystemRights.Delete | FileSystemRights.DeleteSubdirectoriesAndFiles | FileSystemRights.ChangePermissions | FileSystemRights.TakeOwnership;

    private const FileSystemRights RightsToChangeContents =
        RightsToReplaceAnObject | FileSystemRights.WriteData | FileSystemRights.AppendData | FileSystemRights.WriteExtendedAttributes;

    // Generic rights are mapped in an access list unless the entry is inherit-only, but a list can be written by hand.
    private const int GenericWrite = 0x40000000;
    private const int GenericAll = 0x10000000;

    // The executable and the folder it is in are checked for everything that changes contents; the folders above it only for
    // what replaces them, because an account that can add a sibling above has changed nothing the path resolves to.
    private const int ObjectsCheckedForContents = 2;

    private static readonly HashSet<SecurityIdentifier> TrustedAccounts = new()
    {
        new(WellKnownSidType.LocalSystemSid, null),
        new(WellKnownSidType.BuiltinAdministratorsSid, null),
        new(TrustedInstallerSid),
    };

    private static readonly SecurityIdentifier CreatorOwner = new(CreatorOwnerSid);

    /// <inheritdoc />
    public FolderProtection InspectFolder(string executablePath)
    {
        try
        {
            var absolute = Path.GetFullPath(executablePath);
            return IsOnLocalFixedDisk(absolute) ? ProtectionOfChain(PathChain(absolute)) : FolderProtection.NotLocalFixedDisk;
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or ArgumentException or NotSupportedException or InvalidOperationException)
        {
            // The interface promises not to throw: what cannot be read is a refusal.
            return FolderProtection.Unknown;
        }
    }

    private static bool IsOnLocalFixedDisk(string absolutePath)
    {
        var root = Path.GetPathRoot(absolutePath);
        return !string.IsNullOrEmpty(root) && !root.StartsWith(@"\\", StringComparison.Ordinal) && new DriveInfo(root).DriveType == DriveType.Fixed;
    }

    /// <summary>The volume root, every folder below it and the path itself, in that order.</summary>
    private static List<string> PathChain(string absolutePath)
    {
        var chain = new List<string>();
        for (var current = absolutePath; current is not null; current = Path.GetDirectoryName(current))
        {
            chain.Insert(0, current);
        }

        return chain;
    }

    private static FolderProtection ProtectionOfChain(List<string> chain)
    {
        for (var position = 0; position < chain.Count; position++)
        {
            var remaining = chain.Count - position;
            var verdict = InspectObject(chain[position], RightsToCheckAt(isVolumeRoot: position == 0, remaining));
            if (verdict != FolderProtection.AdminOnly)
            {
                return verdict;
            }
        }

        return FolderProtection.AdminOnly;
    }

    private static FileSystemRights RightsToCheckAt(bool isVolumeRoot, int remaining)
    {
        if (remaining <= ObjectsCheckedForContents)
        {
            return RightsToChangeContents;
        }

        // Nobody can delete or rename a volume root, and a data volume lets Authenticated Users modify it, which includes deleting.
        return isVolumeRoot ? RightsToReplaceAnObject & ~FileSystemRights.Delete : RightsToReplaceAnObject;
    }

    private static FolderProtection InspectObject(string path, FileSystemRights rights)
    {
        var info = new FileInfo(path);
        if (!info.Exists && !Directory.Exists(path))
        {
            return FolderProtection.Unknown;
        }

        if (info.Attributes.HasFlag(FileAttributes.ReparsePoint))
        {
            return FolderProtection.Unknown; // a link: where it leads is not what was checked
        }

        var security = ReadSecurity(path);
        if (!TrustedAccounts.Contains(OwnerOf(security)))
        {
            return FolderProtection.WritableByUsers;
        }

        return HasUntrustedWriter(security, rights) ? FolderProtection.WritableByUsers : FolderProtection.AdminOnly;
    }

    private static FileSystemSecurity ReadSecurity(string path) =>
        Directory.Exists(path) ? new DirectoryInfo(path).GetAccessControl() : new FileInfo(path).GetAccessControl();

    private static SecurityIdentifier OwnerOf(FileSystemSecurity security) =>
        security.GetOwner(typeof(SecurityIdentifier)) as SecurityIdentifier ?? throw new InvalidOperationException("the object has no owner");

    /// <summary>
    /// Whether an entry that allows rights, and applies to this object itself, names an account outside the trusted ones. Entries
    /// that deny are not counted: they can be removed by whoever may change the list, which is what is being asked.
    /// </summary>
    private static bool HasUntrustedWriter(FileSystemSecurity security, FileSystemRights rights)
    {
        foreach (FileSystemAccessRule rule in security.GetAccessRules(includeExplicit: true, includeInherited: true, typeof(SecurityIdentifier)))
        {
            var appliesToThisObject = !rule.PropagationFlags.HasFlag(PropagationFlags.InheritOnly);
            var account = (SecurityIdentifier)rule.IdentityReference;
            if (rule.AccessControlType == AccessControlType.Allow && appliesToThisObject && !TrustedAccounts.Contains(account) && account != CreatorOwner && GrantsAny(rule, rights))
            {
                return true;
            }
        }

        return false;
    }

    private static bool GrantsAny(FileSystemAccessRule rule, FileSystemRights rights)
    {
        var mask = (int)rule.FileSystemRights;
        return (rule.FileSystemRights & rights) != 0 || (mask & (GenericWrite | GenericAll)) != 0;
    }

    // WinVerifyTrust

    private const uint TrustENoSignature = 0x800B0100;
    private const uint TrustESubjectFormUnknown = 0x800B0003;
    private const uint TrustEProviderUnknown = 0x800B0001;

    private const uint UiNone = 2;
    private const uint RevokeNone = 0;
    private const uint UnionFile = 1;
    private const uint StateActionIgnore = 0;
    private const uint CacheOnlyUrlRetrieval = 0x1000;
    private static readonly nint InvalidHandle = -1;
    private static readonly Guid GenericVerifyV2 = new("00AAC56B-CD44-11D0-8CC2-00C04FC295EE");

    /// <inheritdoc />
    public FileSignature InspectSignature(string executablePath, string expectedPublisher)
    {
        var status = Verify(executablePath);
        if (status is TrustENoSignature or TrustESubjectFormUnknown or TrustEProviderUnknown)
        {
            return FileSignature.NotSigned;
        }

        if (status != 0)
        {
            return FileSignature.Invalid;
        }

        return PublisherOf(executablePath) == expectedPublisher ? FileSignature.Valid : FileSignature.OtherPublisher;
    }

    private static string? PublisherOf(string executablePath)
    {
        try
        {
            // The replacement, X509CertificateLoader, loads certificate files and cannot read the signer out of a signed program.
#pragma warning disable SYSLIB0057
            using var certificate = new X509Certificate2(X509Certificate.CreateFromSignedFile(executablePath));
#pragma warning restore SYSLIB0057
            return certificate.GetNameInfo(X509NameType.SimpleName, forIssuer: false);
        }
        catch (System.Security.Cryptography.CryptographicException)
        {
            return null; // a signature that checks out but carries no certificate to read is not the publisher's
        }
    }

    /// <summary>The result of WinVerifyTrust for the file's embedded or catalog signature: 0 is a valid one. The user interface and revocation lookups over the network are off.</summary>
    private static uint Verify(string executablePath)
    {
        var path = Marshal.StringToHGlobalUni(executablePath);
        var fileInfo = Marshal.AllocHGlobal(Marshal.SizeOf<WintrustFileInfo>());
        try
        {
            Marshal.StructureToPtr(new WintrustFileInfo { Size = (uint)Marshal.SizeOf<WintrustFileInfo>(), FilePath = path }, fileInfo, fDeleteOld: false);
            var data = new WintrustData
            {
                Size = (uint)Marshal.SizeOf<WintrustData>(),
                UiChoice = UiNone,
                RevocationChecks = RevokeNone,
                UnionChoice = UnionFile,
                File = fileInfo,
                StateAction = StateActionIgnore,
                ProviderFlags = CacheOnlyUrlRetrieval,
            };
            var action = GenericVerifyV2;
            return unchecked((uint)WinVerifyTrust(InvalidHandle, ref action, ref data));
        }
        finally
        {
            Marshal.FreeHGlobal(fileInfo);
            Marshal.FreeHGlobal(path);
        }
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct WintrustFileInfo
    {
        public uint Size;
        public nint FilePath;
        public nint FileHandle;
        public nint KnownSubject;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct WintrustData
    {
        public uint Size;
        public nint PolicyCallbackData;
        public nint SipClientData;
        public uint UiChoice;
        public uint RevocationChecks;
        public uint UnionChoice;
        public nint File;
        public uint StateAction;
        public nint StateData;
        public nint UrlReference;
        public uint ProviderFlags;
        public uint UiContext;
        public nint SignatureSettings;
    }

    [LibraryImport("wintrust.dll")]
    private static partial int WinVerifyTrust(nint window, ref Guid action, ref WintrustData data);
}
