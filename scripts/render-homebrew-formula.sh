#!/usr/bin/env bash
# Render the Homebrew formula for the Linux CLI of a published release. macOS
# uses the cask instead; the formula has its own name so that installing
# without --cask on macOS never picks this Linux-only package.
#
#   scripts/render-homebrew-formula.sh v0.1.2 SHA256SUMS > sharecodex-cli.rb
set -euo pipefail

tag="$1"
sums="$2"
version="${tag#v}"

sha() {
  local s
  s="$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$sums")"
  [[ ${#s} -eq 64 ]] || { echo "no SHA-256 for $1 in $sums" >&2; exit 1; }
  echo "$s"
}
amd64="$(sha sharecodex-linux-amd64.tar.gz)"
arm64="$(sha sharecodex-linux-arm64.tar.gz)"

cat <<FORMULA
class SharecodexCli < Formula
  desc "Track shared Claude Code and Codex subscription quota"
  homepage "https://github.com/KoukeNeko/ShareCodex"
  version "$version"

  livecheck do
    url :stable
    strategy :github_latest
  end

  depends_on :linux

  if Hardware::CPU.arm?
    url "https://github.com/KoukeNeko/ShareCodex/releases/download/v#{version}/sharecodex-linux-arm64.tar.gz"
    sha256 "$arm64"
  else
    url "https://github.com/KoukeNeko/ShareCodex/releases/download/v#{version}/sharecodex-linux-amd64.tar.gz"
    sha256 "$amd64"
  end

  def install
    bin.install "sharecodex"
  end

  def caveats
    <<~EOS
      If you turned on autostart, restart the agent after upgrading:
        sharecodex autostart on
    EOS
  end

  test do
    assert_match "usage:", shell_output("#{bin}/sharecodex help 2>&1", 1)
  end
end
FORMULA
