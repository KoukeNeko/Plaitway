Several VPNs at once from one menu bar item: OpenVPN and WireGuard profiles side by side, with one component owning the routes and DNS.

## Install

**macOS 15 or later, Apple silicon.** Signed with a Developer ID and notarized by Apple.

- Homebrew: `brew install --cask koukeneko/tap/plaitway`
- Or open the dmg and drag **Plaitway** to **Applications**, or unzip the zip and move the app there

The first run offers **Install Helper**; allow Plaitway in **System Settings › General › Login Items & Extensions**. Reinstalling the helper disconnects running profiles.

**Windows (preview).** The command line and the helper run; the helper has an in-memory backend only, and there is no app and no VPN engine yet. The binaries are not signed.

```powershell
scoop bucket add koukeneko https://github.com/KoukeNeko/scoop-bucket
scoop install koukeneko/plaitway
```

[README](https://github.com/KoukeNeko/Plaitway#readme) has the details, the limitations and what is not verified on real hardware yet.
