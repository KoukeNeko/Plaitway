Plaitway for Windows, without the installer
===========================================

This folder is what the MSI installs, as a zip. Nothing is written to the registry or to the Start menu.

1. Put the folder where only administrators can write: for example C:\Program Files\Plaitway-portable
   (copy it there from an elevated Explorer or shell). Not Downloads, not the Desktop: plaitwayd refuses to
   register itself, and refuses to run OpenVPN, from a folder that a standard user could change.

2. In an elevated terminal, in that folder:

     plaitwayd.exe install -start

   This registers and starts the service "PlaitwayHelper" from this folder. (The app's "Install the helper"
   button does the same and asks Windows for permission.)

3. For OpenVPN profiles, add the TAP-Windows6 driver to Windows once, also elevated:

     pnputil /add-driver openvpn\driver\OemVista.inf

   WireGuard profiles need nothing more.

4. Start app\Plaitway.exe. plaitway.exe is the same thing on the command line.

To remove it, in an elevated terminal: plaitwayd.exe uninstall   (add -purge to delete the profiles and settings in
%ProgramData%\Plaitway too), then delete the folder. The TAP driver stays in Windows: it is the same driver as
that of an OpenVPN you may have installed.

The licences are in licenses\ and openvpn\license.txt. The MSI, the same files with an installer, is on the
release page.
