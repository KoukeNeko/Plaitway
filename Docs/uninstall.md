# Uninstall

In this order, so that the helper removes its routes and DNS entries before it goes.

**Uninstall on macOS**, in this order:

1. In Plaitway, delete the profiles you do not want to keep. This also deletes their
   saved credentials from the Keychain
2. **Uninstall Helper…** in Settings. The helper removes its routes and DNS entries
3. Quit Plaitway and remove the app: `brew uninstall --cask koukeneko/tap/plaitway`, or
   move `Plaitway.app` to the Trash. Removing the app before step 2 can leave a root
   helper registered with nothing to stop it
4. The helper's data stays until you delete it, because the profiles hold private keys
   that cannot be made again:
   `sudo rm -rf "/Library/Application Support/Plaitway" /Library/Logs/Plaitway /var/run/plaitway`

[packaging/README.md](../packaging/README.md#uninstall) lists what is left where, and has
the steps for a helper installed with `scripts/dev-install-daemon.sh`.

**Uninstall on Linux**, in this order:

1. In Plaitway, delete the profiles you do not want to keep. This also deletes their
   saved passwords from the Secret Service (GNOME Keyring, KWallet)
2. `sudo apt remove plaitway` stops the helper and removes the program. The profiles stay
   in `/var/lib/plaitway`; `sudo apt purge plaitway` deletes them too, and they hold
   private keys that cannot be made again
3. If you added the apt repository,
   `sudo rm /etc/apt/sources.list.d/plaitway.list /etc/apt/keyrings/plaitway.asc`, then
   `sudo apt update`
4. What the app keeps in your home: `~/.local/state/plaitway`, the window's own
   settings, and with **Launch at Login** on,
   `~/.config/autostart/io.github.koukeneko.Plaitway.desktop`

On Gentoo, `sudo rc-service plaitwayd stop && sudo rc-update del plaitwayd default`, then
`sudo emerge --unmerge net-vpn/plaitway`. Installed with `make install`, there is no uninstall
target: delete the files of the table in [Linux](technical-reference.md#linux) from below `/usr/local`. Installed with
`install.sh --openrc`, stop and remove the service the same way and delete `/etc/init.d/plaitwayd`,
`/etc/logrotate.d/plaitwayd` and the files of the table. The profiles stay in `/var/lib/plaitway`;
to delete them with their private keys as well, `sudo rm -rf /var/lib/plaitway /var/log/plaitwayd.log*`. A helper installed with
`scripts/linux/dev-install-daemon.sh` is removed by
`sudo scripts/linux/dev-uninstall-daemon.sh`, and `--purge` deletes the profiles as well.
