<p align="center">
  <img src="build/appicon.png" alt="ShareCodex" width="128">
</p>

<h1 align="center">ShareCodex</h1>

<p align="center">
  <strong>一份訂閱，多人共用，用量一目了然。</strong><br>
  看清每個共用的 Claude 與 ChatGPT 方案還剩多少，以及誰用了多少。
</p>

<p align="center">
  <a href="README.md">English</a> | 繁體中文
</p>

<p align="center">
  <a href="https://github.com/KoukeNeko/ShareCodex/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/KoukeNeko/ShareCodex?style=for-the-badge&logo=github&label=RELEASE&color=225ED6"></a>
  <a href="https://github.com/KoukeNeko/ShareCodex/releases"><img alt="GitHub downloads" src="https://img.shields.io/github/downloads/KoukeNeko/ShareCodex/total?style=for-the-badge&logo=github&label=DOWNLOADS&color=4CAF50"></a>
  <img alt="macOS | Windows" src="https://img.shields.io/badge/DESKTOP-macOS%20%7C%20Windows-414141?style=for-the-badge&logo=apple&logoColor=white">
  <a href="https://github.com/KoukeNeko/ShareCodex/pkgs/container/sharecodex-server"><img alt="Server image" src="https://img.shields.io/badge/SERVER-ghcr.io-2496ED?style=for-the-badge&logo=docker&logoColor=white"></a>
  <a href="https://github.com/KoukeNeko/ShareCodex/stargazers"><img alt="Stars" src="https://img.shields.io/github/stars/KoukeNeko/ShareCodex?style=for-the-badge&logo=github&label=STARS&color=225ED6"></a>
</p>

<p align="center">
  <a href="https://github.com/KoukeNeko/ShareCodex/releases/latest">下載</a>
  · <a href="#開始使用">開始使用</a>
  · <a href="#架設-server">架設 server</a>
</p>

幾個人共用 Claude Max／Pro 與 ChatGPT Plus 訂閱時，provider 只告訴你一件事：整個帳號用掉多少。
ShareCodex 補上其餘的部分——**每個 5 小時與每週額度還剩多少、每個人分到多少、每個人大約用了多少，
以及用在哪些模型上。**

每位成員在自己電腦上執行一個常駐選單列（macOS）或系統匣（Windows）的小 app。它讀取電腦上原本就有的
Claude Code 與 Codex 紀錄，透過官方 CLI 確認目前登入的帳號，再把 token 用量同步到你自架的 server。

只計算 coding agent 的用量：Claude Code，以及 Codex CLI、桌面 app 與 IDE 擴充功能。
不計算 ChatGPT 網頁聊天。

## 實際畫面

<p align="center">
  <img src="docs/images/popup-light.png" width="49%" alt="桌面 popup（淺色）">
  <img src="docs/images/popup-dark.png" width="49%" alt="桌面 popup（深色）">
</p>

<p align="center">
  <img src="docs/images/admin-overview.png" width="49%" alt="管理介面總覽">
  <img src="docs/images/admin-accounts.png" width="49%" alt="管理介面帳號">
</p>

## 能看到什麼

### 所有共用帳號一眼看完

popup 列出每個共用帳號的 5 小時與每週額度、已用多少、何時重置。數字直接來自 Claude Code 與 Codex，
不是推測。

### 誰用了多少

每位成員有一條估計用量的進度列，並在分配的位置標上刻度。超過分配的人會標示**超出分配**。
有額度消耗、卻對不到任何人紀錄的部分列為**未歸屬**，不會硬算到某個人頭上。

### 用在哪些模型

每個額度視窗也會依模型拆分——請求數、tokens 與估計占比——一眼看出這週是花在 Opus，
還是一長串的 Sonnet。

### 一條連結就能加入

admin 在網頁管理介面產生一次性加入連結。成員把連結貼進 app，照常在 Claude Code 或 Codex
登入共用帳號，就會自動加入該帳號。

### 在背景安靜運作

server 連不上時照常記錄，恢復連線後再上傳。可以設定登入時啟動，開啟時自動更新，也不會跳出任何終端機視窗。

### 在瀏覽器管理

成員、加入連結、帳號名稱、分配權重與裝置，都在 server 的 `/admin/` 管理，不需要進入容器下指令。

## 隱私設計

- 離開電腦的只有 token 數、模型名稱、時間與額度百分比。
- prompt、回應內容、工作目錄與專案路徑從不寫入帳本，也不會上傳。
- 帳號以單向雜湊識別；server 只保留遮罩過的 email（例如 `al***@example.com`），讓 admin 分辨是哪個帳號。
- 不讀取任何憑證檔。帳號身分來自 `claude auth status` 與 Codex 自己的 app server。
- server 由你自己架設，沒有第三方服務、分析或追蹤。

## 開始使用

1. **向架設 ShareCodex server 的人取得加入連結。**
2. **安裝 app**（見[取得 app](#取得-app)），從選單列或系統匣開啟。
3. **貼上加入連結。** 每台電腦各需要一條連結。
4. **照常在 Claude Code 或 Codex 登入共用帳號。** app 看到該帳號後就會出現。
5. **在 app 設定頁啟用 statusLine 擷取**，才能取得 Claude 的額度。原本的 statusLine 照常顯示，
   停用時會還原原本的設定。

加入之前的歷史用量不會上傳。

## 架設 server

需要 Docker。server 本身不處理 TLS，請放在反向代理（例如 Caddy、nginx 或 Cloudflare Tunnel）後面。

從[最新 release](https://github.com/KoukeNeko/ShareCodex/releases/latest) 下載
`sharecodex-server-compose.zip`。它使用已發佈的映像檔 `ghcr.io/koukeneko/sharecodex-server`
（linux/amd64 與 linux/arm64），並固定在該 release 的版本。

```bash
unzip sharecodex-server-compose.zip && cd sharecodex-server-compose
cp .env.example .env    # 設定 POSTGRES_PASSWORD、PUBLIC_URL 與 ADMIN_PASSWORD
docker compose up -d
```

`PUBLIC_URL` 是成員連到 server 的網址，會出現在加入連結裡。升級時，把 `.env` 的 `SHARECODEX_VERSION`
設為新版本（或改用新版 bundle），再執行一次 `docker compose up -d`。

接著開啟 `PUBLIC_URL/admin/`，用 `ADMIN_PASSWORD` 登入：

| 頁面 | 用途 |
|---|---|
| 總覽 | 每個帳號的額度視窗、每位成員的分配與估計用量，以及依模型拆分的用量 |
| 成員 | 新增成員、產生一次性加入連結 |
| 帳號 | 幫帳號命名、設定每位成員的分配權重 |
| 裝置 | 查看各裝置最後同步時間、撤銷裝置 |

管理登入狀態只存在記憶體，重新啟動 server 後需要重新登入。

## 相容性

- **桌面：** macOS（Apple silicon 與 Intel）與 Windows
- **Agent：** 使用 Claude Pro／Max 訂閱的 Claude Code；使用 ChatGPT 訂閱的 Codex CLI、桌面 app 與 IDE 擴充功能
- **Server：** 任何 Docker 主機，linux/amd64 或 linux/arm64
- **介面語言：** 繁體中文

轉接到其他廠商模型的用量——透過 gateway 的 Claude Code，或使用第三方 model provider 的 Codex——
不會計入，因為它不消耗共用的訂閱額度。

## 取得 app

macOS，使用 [Homebrew](https://brew.sh)：

```bash
brew install --cask koukeneko/tap/sharecodex
```

Windows，使用 [Scoop](https://scoop.sh)：

```powershell
scoop bucket add koukeneko https://github.com/KoukeNeko/scoop-bucket
scoop install koukeneko/sharecodex
```

也可以從[最新 GitHub Release](https://github.com/KoukeNeko/ShareCodex/releases/latest) 下載
`ShareCodex-macos-universal.zip` 或 `ShareCodex-windows-amd64.zip`。macOS 版以 Developer ID 簽署並經
Apple 公證。app 會檢查 GitHub 是否有新版本並提示，但不會自行下載或安裝任何東西。更新請執行
`brew upgrade --cask sharecodex` 或 `scoop update sharecodex`。

---

## 技術參考

### 用量如何計算

- **Claude Code**：讀取 `~/.claude/projects/**/*.jsonl` transcript。每個回應以 message ID 與
  request ID 只算一次，output 取最大值（串流中的前幾行是部分值）。恢復的 session 把先前的請求複製到新檔案時，
  也只算一次。
- **Codex**：讀取 `~/.codex/sessions` 與 `archived_sessions` 的 rollout。有逐次請求紀錄時直接使用；
  舊格式改用累計值的差額。
- **額度**：Codex 的額度視窗來自 rollout 與 `codex app-server`；Claude 的來自 statusLine 輸入，
  由 app 透過一個小 shim 擷取。
- **帳號**：每筆事件對應到該裝置當下登入的帳號。

### 占比如何估算

每位成員的分配為 `權重 ÷ 該帳號所有成員權重總和`。預設權重都是 1；權重 0 表示不分配，但保留用量紀錄。

provider 只回報整個帳號用了幾 %。每個人——以及每個模型——的部分，依同一視窗內的 API 等價成本比例估算，
因此一律標示「估計」。`internal/attribution/pricing.json` 的價格只用來決定模型之間的相對權重，不代表實際帳單。

### 開發

需要 Go 1.27、Node 24、pnpm 與 Wails CLI
（`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.25`）。

```bash
go test ./...                     # server 整合測試需另設 TEST_DATABASE_URL
wails3 task dev                   # 開發模式執行桌面 app
wails3 task package VERSION=0.1.0 # 打包（macOS 產生 bin/ShareCodex.app）
go run . scan                     # 印出本機每日 token 用量，不寫入資料庫
```

`SHARECODEX_HOME` 可以指定另一個資料目錄，方便在同一台電腦模擬多台裝置。若要從原始碼建置 server，
在 `deploy/` 執行 `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`。

相依方向只能往內：

```
domain  ←  provider adapters  ←  client / server  ←  Wails / HTTP / SQL / OS
```

`internal/account`、`identity`、`usage`、`quota`、`attribution` 不可以依賴儲存、網路、桌面框架或
provider 程式碼，由 `internal/architecture_test.go` 檢查。設計與驗證紀錄見 [docs/plan.md](docs/plan.md)
與 [docs/spikes.md](docs/spikes.md)。

### 發佈

推送 `v*` tag 會觸發 `.github/workflows/release.yml`，產生 macOS universal `.app`、Windows `.exe`、
Linux server binary 與 Docker Compose bundle，把 server 映像檔推送到 `ghcr.io/koukeneko/sharecodex-server`，
並建立附 `SHA256SUMS` 的 GitHub Release。接著執行 `.github/workflows/packages.yml`，更新
[KoukeNeko/homebrew-tap](https://github.com/KoukeNeko/homebrew-tap) 的 cask 與
[KoukeNeko/scoop-bucket](https://github.com/KoukeNeko/scoop-bucket) 的 manifest；也可以手動執行來補發某個 release。

簽署在設定對應的 repository secrets 後才會執行；沒有設定時，macOS 版只有 ad-hoc 簽署，
第一次開啟需在「系統設定 › 隱私權與安全性」允許。

| Secret | 用途 |
|---|---|
| `MACOS_CERTIFICATE_P12_BASE64`（base64 編碼的 `.p12`，內含 Developer ID Application 憑證與私鑰）、`MACOS_CERT_PASSWORD` | Developer ID 簽署 |
| `ASC_KEY_P8_BASE64`（base64 編碼的 App Store Connect API 金鑰 `.p8`）、`APPLE_API_KEY_ID`、`APPLE_API_ISSUER_ID` | 公證 |
| `WINDOWS_CERT_PFX`（base64）、`WINDOWS_CERT_PASSWORD` | Windows 程式碼簽章 |
| `HOMEBREW_TAP_TOKEN`、`SCOOP_BUCKET_TOKEN`（對 tap 與 bucket 具 Contents 讀寫權限的 fine-grained token） | 發佈套件；必要 |

<p>
  <a href="https://github.com/KoukeNeko/ShareCodex/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/KoukeNeko/ShareCodex/ci.yml?branch=main&style=for-the-badge&logo=githubactions&logoColor=white&label=CI"></a>
  <img alt="Go" src="https://img.shields.io/badge/GO-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white">
  <img alt="Wails" src="https://img.shields.io/badge/WAILS-v3-DF0000?style=for-the-badge">
  <img alt="Svelte" src="https://img.shields.io/badge/SVELTE-5-FF3E00?style=for-the-badge&logo=svelte&logoColor=white">
  <img alt="PostgreSQL" src="https://img.shields.io/badge/POSTGRESQL-17-4169E1?style=for-the-badge&logo=postgresql&logoColor=white">
</p>

## 支持

如果 ShareCodex 對你有幫助，可以支持開發：

<a href="https://buymeacoffee.com/doershing"><img alt="Buy Me a Coffee" src="https://img.shields.io/badge/Buy%20Me%20a%20Coffee-doershing-FFDD00?style=for-the-badge&logo=buymeacoffee&logoColor=black"></a>
