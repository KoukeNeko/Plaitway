## Install

**macOS 15 or later, Apple silicon.** Signed with a Developer ID and notarized by Apple.

- Homebrew: `brew install --cask koukeneko/tap/plaitway`
- Or open the dmg and drag **Plaitway** to **Applications**, or unzip the zip and move the app there

The first run offers **Install Helper**; allow Plaitway in **System Settings › General › Login Items & Extensions**. Reinstalling the helper disconnects running profiles.

**Linux with systemd (Ubuntu 24.04 or 26.04, Debian 13).** The `.deb` for amd64 and arm64 is attached a few minutes after the macOS files, with `SHA256SUMS-linux`.

- With the apt repository added (the [README](https://github.com/KoukeNeko/Plaitway#readme) has the commands), `sudo apt install plaitway` installs it and `sudo apt upgrade` brings the later versions
- Or `sudo apt install ./plaitway_<version>_<arch>.deb` installs OpenVPN and the GTK libraries and starts the helper, `plaitwayd.service`
- Open **Plaitway** from the application menu, or run `plaitway-app`; `plaitway` is the command line client

[README](https://github.com/KoukeNeko/Plaitway#readme) has the details, the limitations and what is not verified on real hardware yet.
