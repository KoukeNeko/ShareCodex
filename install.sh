#!/bin/sh
# Install or update ShareCodex from the latest GitHub release: the menu bar
# app on macOS, the sharecodex CLI on Linux. Run it again to update.
#
#   curl -fsSL https://raw.githubusercontent.com/KoukeNeko/ShareCodex/main/install.sh | sh
#
# SHARECODEX_VERSION=0.2.1 installs that release instead of the latest.
set -eu

repo="KoukeNeko/ShareCodex"
bundle_id="io.github.koukeneko.sharecodex"

fail() {
  echo "install.sh: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

case "$(uname -s)" in
  Darwin) os=macos ;;
  Linux) os=linux ;;
  MINGW* | MSYS* | CYGWIN*)
    exec powershell -NoProfile -ExecutionPolicy Bypass -Command \
      "irm https://raw.githubusercontent.com/$repo/main/install.ps1 | iex" ;;
  *) fail "unsupported system: $(uname -s)" ;;
esac

need curl
need tar

version="${SHARECODEX_VERSION:-}"
if [ -z "$version" ]; then
  # The latest-release page redirects to its tag, which avoids the API rate limit.
  latest="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")"
  version="${latest##*/}"
fi
tag="v${version#v}"
case "$tag" in
  v[0-9]*) ;;
  *) fail "could not determine the release to install" ;;
esac

if [ "$os" = macos ]; then
  if command -v brew >/dev/null 2>&1 && brew list --cask sharecodex >/dev/null 2>&1; then
    fail "ShareCodex was installed with Homebrew; update it with: brew update && brew upgrade --cask sharecodex"
  fi
  asset="ShareCodex-macos-universal.zip"
else
  if command -v brew >/dev/null 2>&1 && brew list sharecodex-cli >/dev/null 2>&1; then
    fail "sharecodex was installed with Homebrew; update it with: brew update && brew upgrade sharecodex-cli"
  fi
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
  asset="sharecodex-linux-$arch.tar.gz"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ShareCodex $tag ($asset)"
base="https://github.com/$repo/releases/download/$tag"
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

expected="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "SHA-256 mismatch for $asset"

if [ "$os" = macos ]; then
  dest="/Applications"
  [ -w "$dest" ] || dest="$HOME/Applications"
  mkdir -p "$dest"
  # Replacing the bundle under a running app would leave the old one running.
  if pgrep -xq ShareCodex; then
    osascript -e "tell application id \"$bundle_id\" to quit"
    while pgrep -xq ShareCodex; do sleep 1; done
  fi
  ditto -x -k "$tmp/$asset" "$tmp/app"
  rm -rf "$dest/ShareCodex.app"
  mv "$tmp/app/ShareCodex.app" "$dest/ShareCodex.app"
  echo "Installed $dest/ShareCodex.app"
  open "$dest/ShareCodex.app"
  exit 0
fi

dest="$HOME/.local/bin"
mkdir -p "$dest"
tar -xzf "$tmp/$asset" -C "$tmp" sharecodex
# Moving over the old binary is atomic, so a running agent keeps its copy.
mv "$tmp/sharecodex" "$dest/sharecodex"
chmod 755 "$dest/sharecodex"
echo "Installed $dest/sharecodex"

if command -v systemctl >/dev/null 2>&1 && systemctl --user is-enabled sharecodex.service >/dev/null 2>&1; then
  systemctl --user restart sharecodex.service
  echo "Restarted the autostart service"
fi

case ":$PATH:" in
  *":$dest:"*) ;;
  *) echo "Add $dest to your PATH to run sharecodex" ;;
esac
