# Install or update the ShareCodex tray app from the latest GitHub release.
# Run it again to update.
#
#   irm https://raw.githubusercontent.com/KoukeNeko/ShareCodex/main/install.ps1 | iex
#
# $env:SHARECODEX_VERSION = '0.2.1' installs that release instead of the latest.

# A script block keeps variables out of the caller's session under iex, and
# throw (not exit) reports failures without closing the caller's window.
& {
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'
  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

  $repo = 'KoukeNeko/ShareCodex'

  $scoop = if ($env:SCOOP) { $env:SCOOP } else { Join-Path $env:USERPROFILE 'scoop' }
  if (Test-Path (Join-Path $scoop 'apps\sharecodex')) {
    throw 'ShareCodex was installed with Scoop; update it with: scoop update; scoop update sharecodex'
  }

  $arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    'X64' { 'amd64' }
    'Arm64' { 'arm64' }
    default { throw "unsupported architecture: $_" }
  }
  $asset = "ShareCodex-windows-$arch.zip"

  $version = $env:SHARECODEX_VERSION
  if (-not $version) {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
  }
  $tag = 'v' + $version.TrimStart('v')
  if ($tag -notmatch '^v\d') { throw 'could not determine the release to install' }

  $tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
  New-Item -ItemType Directory $tmp | Out-Null
  try {
    Write-Host "Downloading ShareCodex $tag ($asset)"
    $base = "https://github.com/$repo/releases/download/$tag"
    Invoke-WebRequest -UseBasicParsing "$base/$asset" -OutFile "$tmp\$asset"
    Invoke-WebRequest -UseBasicParsing "$base/SHA256SUMS" -OutFile "$tmp\SHA256SUMS"

    $expected = Get-Content "$tmp\SHA256SUMS" |
      ForEach-Object { $hash, $name = -split $_; if ($name -eq $asset -or $name -eq "*$asset") { $hash } }
    $actual = (Get-FileHash -Algorithm SHA256 "$tmp\$asset").Hash
    if (-not $expected -or $expected -ne $actual) { throw "SHA-256 mismatch for $asset" }

    $dest = Join-Path $env:LOCALAPPDATA 'Programs\ShareCodex'
    $exe = Join-Path $dest 'ShareCodex.exe'
    # Windows won't replace a running executable.
    Get-Process ShareCodex -ErrorAction SilentlyContinue |
      Where-Object { $_.Path -eq $exe } | Stop-Process -Force -PassThru | Wait-Process
    New-Item -ItemType Directory -Force $dest | Out-Null
    Expand-Archive -Force "$tmp\$asset" $dest

    $shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut(
      (Join-Path ([Environment]::GetFolderPath('Programs')) 'ShareCodex.lnk'))
    $shortcut.TargetPath = $exe
    $shortcut.Save()

    Write-Host "Installed $exe"
    Start-Process $exe
  } finally {
    Remove-Item -Recurse -Force $tmp
  }
}
