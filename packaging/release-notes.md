## Install

**macOS 15 or later, Apple silicon.** Signed with a Developer ID and notarized by Apple.

- Homebrew: `brew install --cask koukeneko/tap/plaitway`
- Or open the dmg and drag **Plaitway** to **Applications**, or unzip the zip and move the app there

The first run offers **Install Helper**; allow Plaitway in **System Settings › General › Login Items & Extensions**. Reinstalling the helper disconnects running profiles.

**Linux with systemd (Ubuntu 24.04 or 26.04, Debian 13).** The `.deb` for amd64 and arm64 is attached a few minutes after the macOS files, with `SHA256SUMS-linux`.

- Add the apt repository once, then install; `sudo apt upgrade` brings the later versions:

```sh
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://koukeneko.github.io/Plaitway/key.asc -o /etc/apt/keyrings/plaitway.asc
echo "deb [signed-by=/etc/apt/keyrings/plaitway.asc] https://koukeneko.github.io/Plaitway stable main" | sudo tee /etc/apt/sources.list.d/plaitway.list
sudo chmod 0644 /etc/apt/keyrings/plaitway.asc /etc/apt/sources.list.d/plaitway.list
sudo apt update && sudo apt install plaitway
```

- Or `sudo apt install ./plaitway_<version>_<arch>.deb` for a file you downloaded; either way it installs OpenVPN and the GTK libraries and starts the helper, `plaitwayd.service`
- Open **Plaitway** from the application menu, or run `plaitway-app`; `plaitway` is the command line client

**Linux with OpenRC (Gentoo).** No package is attached: the live ebuild in the repository builds the head of `main` with the Go of the system (`dev-lang/go` 1.27.1 or later) and fetches the Go modules while it unpacks:

```sh
git clone https://github.com/KoukeNeko/Plaitway && cd Plaitway
printf '[plaitway]\nlocation = %s\n' "$PWD/packaging/linux/gentoo" | sudo tee /etc/portage/repos.conf/plaitway.conf
echo '=net-vpn/plaitway-9999 **' | sudo tee -a /etc/portage/package.accept_keywords/plaitway
sudo emerge net-vpn/plaitway
sudo rc-update add plaitwayd default && sudo rc-service plaitwayd start
```

- DNS settings of a full tunnel go through openresolv, which the ebuild installs; with NetworkManager set `rc-manager=resolvconf` in its `[main]` section

**Windows 11, x64.** Windows 10 1809 is the lowest the package accepts; it has not been tried. `Plaitway-<version>-x64.msi`, with `SHA256SUMS-windows`, is attached a few minutes after the macOS files. The package is not signed yet, so SmartScreen asks for confirmation at the first run.

- Run the MSI, or `msiexec /i Plaitway-<version>-x64.msi /qn` from an elevated shell; it installs the helper as the service `PlaitwayHelper` and puts **Plaitway** in the Start menu
- Or with Scoop, which asks Windows for administrator rights: `scoop bucket add koukeneko https://github.com/KoukeNeko/scoop-bucket`, then `scoop install koukeneko/plaitway`
- The app follows the language of Windows; **Settings › Language** changes it
- WireGuard profiles need nothing else. OpenVPN profiles need an OpenVPN installation with the TAP-Windows6 driver, which is not part of the package
- `plaitway` is the command line client, in `C:\Program Files\Plaitway`

## Update

**macOS.** `brew upgrade --cask koukeneko/tap/plaitway`, or replace **Plaitway** in **Applications** with the new one. When the helper is older than the app, the app offers **Reinstall Helper**; that disconnects running profiles.

**Linux.** With the apt repository added, `sudo apt update && sudo apt upgrade`. Without it, `sudo apt install ./plaitway_<version>_<arch>.deb` over the old one. The upgrade restarts the helper, which disconnects running profiles; the stored profiles stay. Quit **Plaitway** and open it again to get the new window.

**Windows.** `scoop update plaitway`, or run the newer MSI over the old one; it stops the service, replaces the files and starts it again, which disconnects running profiles. Removing the package keeps `%ProgramData%\Plaitway`; `msiexec /x … PLAITWAY_PURGE_DATA=1` deletes it too.

**Gentoo.** `sudo emerge @live-rebuild`, then `sudo rc-service plaitwayd restart`, which disconnects running profiles; the stored profiles stay.

[README](https://github.com/KoukeNeko/Plaitway#readme) has the details, how to uninstall, the limitations and what is not verified on real hardware yet.
