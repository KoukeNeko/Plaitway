#!/usr/bin/env bash
# Renders the Homebrew cask for a published release.
#
#   scripts/render-homebrew-cask.sh v0.2.0 SHA256SUMS > plaitway.rb
set -euo pipefail

tag="$1"
sums="$2"
version="${tag#v}"
asset="Plaitway-$version.zip"
sha="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$sums")"
[[ ${#sha} -eq 64 ]] || { echo "no SHA-256 for $asset in $sums" >&2; exit 1; }

cat <<CASK
cask "plaitway" do
  version "$version"
  sha256 "$sha"

  url "https://github.com/KoukeNeko/Plaitway/releases/download/v#{version}/Plaitway-#{version}.zip"
  name "Plaitway"
  desc "Run several OpenVPN and WireGuard VPNs at once from the menu bar"
  homepage "https://github.com/KoukeNeko/Plaitway"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on arch: :arm64
  depends_on macos: ">= :sequoia"

  app "Plaitway.app"
  binary "#{appdir}/Plaitway.app/Contents/Resources/bin/plaitway"

  uninstall quit: "io.github.koukeneko.plaitway"

  caveats <<~EOS
    Before uninstalling, choose Uninstall Helper… in Plaitway's Settings. Removing the app
    first can leave a root helper registered with nothing to stop it.
  EOS
end
CASK
