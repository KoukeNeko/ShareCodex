#!/usr/bin/env bash
# Render the Homebrew cask for a published release.
#
#   scripts/render-homebrew-cask.sh v0.1.2 SHA256SUMS > sharecodex.rb
set -euo pipefail

tag="$1"
sums="$2"
version="${tag#v}"
asset="ShareCodex-macos-universal.zip"
sha="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$sums")"
[[ ${#sha} -eq 64 ]] || { echo "no SHA-256 for $asset in $sums" >&2; exit 1; }

cat <<CASK
cask "sharecodex" do
  version "$version"
  sha256 "$sha"

  url "https://github.com/KoukeNeko/ShareCodex/releases/download/v#{version}/$asset"
  name "ShareCodex"
  desc "Track shared Claude Code and Codex subscription quota"
  homepage "https://github.com/KoukeNeko/ShareCodex"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: :monterey

  app "ShareCodex.app"

  uninstall launchctl: "io.github.koukeneko.sharecodex",
            quit:      "io.github.koukeneko.sharecodex"

  zap trash: [
    "~/Library/Application Support/ShareCodex",
    "~/Library/LaunchAgents/io.github.koukeneko.sharecodex.plist",
  ]
end
CASK
