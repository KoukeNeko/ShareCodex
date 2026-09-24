# ShareCodex

[English](README.md) | 繁體中文

幾個人共用 Claude Max／Pro 與 ChatGPT Plus 訂閱時，用來回答兩件事：

- 每個共用帳號的 5 小時與每週額度還剩多少。
- 每個人分到多少、估計用了多少。

只計算 coding agent 的用量（Claude Code 與 Codex CLI／Desktop／IDE），不計算 ChatGPT 網頁聊天。

- 設計與決策：[docs/plan.md](docs/plan.md)
- 實測驗證紀錄：[docs/spikes.md](docs/spikes.md)

## 運作方式

每位成員的電腦（macOS 選單列、Windows 系統匣）跑一個桌面 app。它讀取本機的 Claude Code transcript 與 Codex rollout 紀錄，透過官方 CLI 確認目前登入的帳號（不讀取任何憑證檔），再把 token 用量與額度快照上傳到自架的 server。server 彙整所有人的資料，算出每個帳號的額度與每人占比。

上傳的只有 token 數、模型、時間與額度百分比；prompt、工作目錄與專案路徑都不會離開本機。

## 架設 server

需要 Docker。server 本身不處理 TLS，請放在反向代理（例如 Caddy、nginx）後面。

```bash
cd deploy
cp .env.example .env    # 設定 POSTGRES_PASSWORD 與 PUBLIC_URL
docker compose up -d
```

`PUBLIC_URL` 是成員連到 server 的網址，會出現在加入連結裡。

## 管理

所有管理動作都在 server 上用 CLI 執行：

```bash
docker compose exec server sharecodex-server admin person add --name 江董 --admin
```

| 指令 | 用途 |
|---|---|
| `admin person add --name NAME [--admin]` | 新增成員 |
| `admin person list` | 列出成員 |
| `admin invite --person PERSON [--ttl 24h]` | 產生一次性加入連結 |
| `admin account list` | 列出帳號（自動建立，`HINT` 是遮罩過的 email） |
| `admin account label --account ACCOUNT --label LABEL` | 幫帳號命名 |
| `admin share list --account ACCOUNT` | 查看分配 |
| `admin share set --account ACCOUNT --person PERSON --weight W` | 調整分配權重 |
| `admin device list` | 列出裝置 |
| `admin device revoke --id DEVICE` | 撤銷裝置 |

`PERSON` 與 `ACCOUNT` 可以填 ID，也可以填完整名稱。

## 成員加入

1. admin 執行 `admin person add` 建立成員，再用 `admin invite` 產生加入連結（24 小時內有效、只能用一次），傳給對方。
2. 成員安裝桌面 app，在 popup 貼上加入連結。同一人的每台電腦各需要一條連結。
3. 成員在自己電腦上用共用帳號登入 Claude Code 或 Codex。app 第一次看到該帳號時，server 自動建立帳號與成員關係（權重 1），不需要 admin 手動設定。
4. 要取得 Claude 的額度，需在 app 的設定頁啟用「statusLine 擷取」。app 會修改 `~/.claude/settings.json`（先備份），原本的 statusLine 照常顯示，停用時會還原。

加入之前的歷史用量不會上傳。撤銷裝置後，該裝置已上傳的紀錄仍保留。

## 額度分配

- **分配**：每位成員在每個帳號上都有一個權重，分到的比例為 `權重 ÷ 該帳號所有成員權重總和`。預設全部是 1，也就是平均分配；權重設為 0 表示不分配，但保留該成員的用量紀錄。
- **估計用量**：provider 只回報整個帳號用了幾 %。每人的部分依同一視窗內的 API 等價成本比例估算，因此一律標示「估計」。視窗內有額度消耗、卻沒有任何人的紀錄時，那部分列為「未歸屬」。
- 分配只是參考，沒有強制力，超出時會標示「超出分配」。

## 開發

需要 Go 1.27、Node 24、pnpm、Wails CLI（`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.25`）。

```bash
go test ./...                     # server 整合測試需另設 TEST_DATABASE_URL
wails3 task dev                   # 開發模式執行桌面 app
wails3 task package VERSION=0.1.0 # 打包（macOS 產生 bin/ShareCodex.app）
go run . scan                     # 印出本機每日 token 用量，不寫入資料庫
```

`SHARECODEX_HOME` 可以指定另一個資料目錄，方便在同一台電腦模擬多台裝置。

### 相依規則

```
domain  ←  provider adapters  ←  client / server  ←  Wails / HTTP / SQL / OS
```

`internal/account`、`identity`、`usage`、`quota`、`attribution` 不可以依賴儲存、網路、桌面框架或 provider 程式碼，由 `internal/architecture_test.go` 檢查。

## 發佈

推送 `v*` tag 會觸發 `.github/workflows/release.yml`，產生 macOS universal `.app`、Windows `.exe` 與 Linux server binary，並建立 GitHub Release。

簽署在設定下列 repository secrets 後才會執行；沒有設定時，macOS 版只有 ad-hoc 簽署，第一次開啟需在「系統設定 › 隱私權與安全性」允許。

| Secret | 用途 |
|---|---|
| `MACOS_CERTIFICATE_P12_BASE64`（base64 編碼的 `.p12`，內含 Developer ID Application 憑證與私鑰）、`MACOS_CERT_PASSWORD` | Developer ID 簽署 |
| `ASC_KEY_P8_BASE64`（base64 編碼的 App Store Connect API 金鑰 `.p8`）、`APPLE_API_KEY_ID`、`APPLE_API_ISSUER_ID` | 公證 |
| `WINDOWS_CERT_PFX`（base64）、`WINDOWS_CERT_PASSWORD` | Windows 程式碼簽章 |
