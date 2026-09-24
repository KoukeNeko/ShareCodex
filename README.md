# ShareCodex

English | [繁體中文](README.zh-TW.md)

When a few people share Claude Max/Pro and ChatGPT Plus subscriptions, ShareCodex answers two questions:

- How much of each shared account's 5-hour and weekly quota is left.
- How much each person is allotted, and roughly how much each has used.

Only coding-agent usage counts (Claude Code, and the Codex CLI, desktop app and IDE extension). ChatGPT web chat is not tracked.

- Design and decisions: [docs/plan.md](docs/plan.md) (Traditional Chinese)
- Verification notes: [docs/spikes.md](docs/spikes.md) (Traditional Chinese)

## How it works

Each member runs a desktop app (macOS menu bar, Windows system tray). It reads the local Claude Code transcripts and Codex rollout logs, asks the official CLIs which account is signed in (no credential files are read), and uploads token usage and quota snapshots to a self-hosted server. The server combines everyone's data into each account's quota and each person's share.

Only token counts, model names, timestamps and quota percentages are uploaded. Prompts, working directories and project paths never leave the device.

## Running the server

Requires Docker. The server does not terminate TLS; put it behind a reverse proxy such as Caddy or nginx.

Download `sharecodex-server-compose.zip` from the [latest release](https://github.com/KoukeNeko/ShareCodex/releases/latest). It runs the published image `ghcr.io/koukeneko/sharecodex-server` (linux/amd64 and linux/arm64), pinned to that release.

```bash
unzip sharecodex-server-compose.zip && cd sharecodex-server-compose
cp .env.example .env    # set POSTGRES_PASSWORD, PUBLIC_URL and ADMIN_PASSWORD
docker compose up -d
```

`PUBLIC_URL` is the address members reach the server at. It appears in join links. To upgrade, set `SHARECODEX_VERSION` in `.env` to the new release (or use a newer bundle) and run `docker compose up -d` again.

To build the server from a checkout instead, run `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build` in `deploy/`.

## Administration

Open `PUBLIC_URL/admin/` and sign in with `ADMIN_PASSWORD` from `.env`. The console has four pages:

| Page | What you do there |
|---|---|
| Overview | See every account's 5-hour and weekly quota, and each member's allotment and estimated usage |
| Members | Add members and create single-use join links |
| Accounts | Rename accounts and set each member's allotment weight |
| Devices | See each device's last sync and revoke devices |

Accounts are created automatically; the masked email next to each one tells them apart. Admin sessions are kept in memory, so restarting the server signs the admin out.

## Joining

1. An admin adds the member on the Members page, then clicks 產生加入連結 to get a join link (valid for 24 hours, single use) and sends it to them.
2. The member installs the desktop app and pastes the join link into the popup. Each of a person's computers needs its own link.
3. The member signs in to the shared account in Claude Code or Codex on their own computer. The first time the app sees that account, the server creates the account and the membership (weight 1). No admin setup is needed.
4. To get Claude's quota, the member turns on statusLine capture on the app's settings page. The app edits `~/.claude/settings.json` after backing it up. The member's existing statusLine keeps working, and turning the feature off restores the original.

Usage from before joining is not uploaded. Revoking a device keeps the records it already uploaded.

## Quota allotment

- **Allotment:** each member has a weight on each account. Their share is `weight ÷ sum of all members' weights on that account`. All weights default to 1, which splits the quota evenly. A weight of 0 removes a member from the allotment but keeps their usage records.
- **Estimated usage:** providers report only the percentage used for the whole account. Each person's part is estimated from their share of API-equivalent cost within the same window, so it is always labelled as an estimate. Quota consumed in a window with no matching records from anyone is shown as unattributed.
- Allotments are informational and are not enforced. A member who uses more than their allotment is marked as over.

## Development

Requires Go 1.27, Node 24, pnpm, and the Wails CLI (`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.25`).

```bash
go test ./...                     # server integration tests also need TEST_DATABASE_URL
wails3 task dev                   # run the desktop app in development mode
wails3 task package VERSION=0.1.0 # package the app (bin/ShareCodex.app on macOS)
go run . scan                     # print local daily token totals without writing to the database
```

Set `SHARECODEX_HOME` to use another data directory, for example to simulate several devices on one computer.

### Dependency rule

```
domain  ←  provider adapters  ←  client / server  ←  Wails / HTTP / SQL / OS
```

`internal/account`, `identity`, `usage`, `quota` and `attribution` must not depend on storage, networking, the desktop framework or provider code. `internal/architecture_test.go` enforces this.

## Releases

Pushing a `v*` tag runs `.github/workflows/release.yml`, which builds a universal macOS `.app`, a Windows `.exe`, Linux server binaries and the Docker Compose bundle, pushes the server image to `ghcr.io/koukeneko/sharecodex-server`, and creates a GitHub Release.

Code signing runs only when the repository secrets below are set. Without them, the macOS app is only ad-hoc signed, and users must allow it the first time in System Settings › Privacy & Security.

| Secret | Purpose |
|---|---|
| `MACOS_CERTIFICATE_P12_BASE64` (base64 of a `.p12` containing a Developer ID Application certificate and its key), `MACOS_CERT_PASSWORD` | Developer ID signing |
| `ASC_KEY_P8_BASE64` (base64 App Store Connect API key `.p8`), `APPLE_API_KEY_ID`, `APPLE_API_ISSUER_ID` | Notarization |
| `WINDOWS_CERT_PFX` (base64), `WINDOWS_CERT_PASSWORD` | Windows code signing |
