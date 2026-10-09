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

## Update

**macOS.** `brew upgrade --cask koukeneko/tap/plaitway`, or replace **Plaitway** in **Applications** with the new one. When the helper is older than the app, the app offers **Reinstall Helper**; that disconnects running profiles.

**Linux.** With the apt repository added, `sudo apt update && sudo apt upgrade`. Without it, `sudo apt install ./plaitway_<version>_<arch>.deb` over the old one. The upgrade restarts the helper, which disconnects running profiles; the stored profiles stay. Quit **Plaitway** and open it again to get the new window.

[README](https://github.com/KoukeNeko/Plaitway#readme) has the details, how to uninstall, the limitations and what is not verified on real hardware yet.
