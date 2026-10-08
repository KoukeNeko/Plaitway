namespace Plaitway.AppCore.Helper;

/// <summary>What the helper keeps on disk, for the places that tell the user where to look. The service's own constants say the same (packaging/windows/service/README.md).</summary>
public static class HelperPaths
{
    private const string ProductFolder = "Plaitway";
    private const string LogFolder = "Logs";
    private const string LogFileName = "plaitwayd.log";

    /// <summary>The service's name in the Service Control Manager.</summary>
    public const string ServiceName = "PlaitwayHelper";

    /// <summary>The folder with the profiles, readable by SYSTEM and administrators only. An uninstall leaves it.</summary>
    public static string StateDirectory { get; } = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), ProductFolder);

    /// <summary>The helper's log, readable by SYSTEM and administrators only.</summary>
    public static string LogPath { get; } = Path.Combine(StateDirectory, LogFolder, LogFileName);
}
