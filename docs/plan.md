# Quota Pool — 專案架構與實作計畫

## Context

幾個人共用少數 Claude Max 與 ChatGPT Plus（Codex）訂閱帳號，但目前沒辦法回答兩個問題：「每個帳號還剩多少 5h / weekly 額度」和「每個人各用了多少」。這個專案在每位成員的 Windows / macOS 電腦上裝一個常駐於系統匣／選單列的 client。client 讀取 Claude Code 與 Codex 的本機紀錄，上傳到自架的中央 Server，彙整成共用帳本。

**已定案的範圍**
- 只計算 AI coding agent 的用量：Claude Code 與 Codex（CLI、Desktop、IDE）。**不計算 ChatGPT 網頁聊天。**
- 服務條款風險由使用者承擔（已確認）。
- Server：自架，用 Docker Compose 跑 Go Server 與 Postgres。
- 可見度：所有成員互相看得到用量；只有 admin 能管理帳號、成員和裝置。
- 桌面：Wails v3 beta，鎖定 `v3.0.0-beta.25`。前端：Vite + Svelte + TypeScript。

## 已實測的事實（本機 Codex 0.154.0、Claude Code 2.1.281）

這些事實推翻或修正了先前 ChatGPT 對話中的架構假設：

1. **Codex 有逐次請求的用量紀錄。** `type:"token_usage_record"` 帶有 `response_id`，可以直接當去重鍵，不需要從累計值推算差額。欄位包括 `usage.{input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_output_tokens}`、`session_id` 和 `turn_id`。
2. **Codex 的額度資料可以被動取得。** 每個 `event_msg/token_count` 都帶有 `rate_limits`：`primary{used_percent, window_minutes:300, resets_at}`、`secondary{…window_minutes:10080…}`、`credits`、`plan_type:"plus"`。所以不必主動呼叫 RPC 也能得到額度快照。
3. **Codex session 不一定連到 OpenAI。** `session_meta.payload.model_provider` 可能是 `ollama-launch` 等第三方 provider，**必須過濾，只收 `openai`**。另外，`originator` 有 `Codex Desktop`、`codex-tui`、`codex_vscode`、`codex_work_desktop`、甚至 `Claude Code` 等值，可以當作「來源介面」保存。
4. **模型名稱來自 `turn_context.payload.model`。** 這與 ccusage 的做法一致。
5. **Claude Code 的 statusLine 輸入有額度資料。** 欄位包括 `rate_limits.five_hour / seven_day {used_percentage, resets_at}`、`session_id` 和 `transcript_path`，但只有 Pro/Max 帳號會有，而且要等 session 中第一個 API 回應之後才出現。
6. **Claude 用量改從 transcript JSONL 讀取**（`~/.claude/projects/**/*.jsonl`，也就是 ccusage 的做法），不用 OTel。這樣不需要改動每個人的環境變數。去重鍵是 `message.id + requestId`。
7. **帳號身分可以從官方介面取得，不需要讀取憑證檔。** Claude 用 `claude auth status --json`（`orgId`、`email`）；Codex 用 app-server 的 `account/read` 與 `account/rateLimits/read`（`accountId`、`email`）。`codex login status` 沒有帳號識別，不採用（見 `docs/spikes.md`）。**本專案絕不讀取 `auth.json`、`.credentials` 或 keychain 裡的 OAuth token。**

## 架構總覽

```
┌──────────── Desktop client（每台 Win/macOS）────────────┐
│ Claude Code ──transcript JSONL──┐                      │
│ Claude Code ──statusLine shim───┤→ provider adapters   │
│ Codex ────────rollout JSONL─────┘        │             │
│ claude auth status / codex account/read → account      │
│                                          ▼  timeline   │
│                      agent（編排） ⇄ local SQLite       │
│                          │   └ outbox                  │
│                   Wails tray/menu-bar popup（Svelte）   │
└──────────────────────────┼─────────────────────────────┘
                           │ HTTPS batch（至少一次送達）
                ┌──────────▼──────────┐
                │ sharecodex-server   │── Postgres
                │ /internal/api/v1/*  │
                └─────────────────────┘
```

依賴方向只能往內：`domain（account/identity/usage/quota/attribution）` ← `provider adapters` ← `client/server` ← `Wails/HTTP/SQL/OS`。domain package 不可以 import `database/sql`、`net/http`、Wails，也不可以使用 `runtime.GOOS`。

## Repository 結構

Wails v3 的建置（Taskfile、bindings 產生、`embed frontend/dist`）預設 main package 在 repo 根目錄，所以桌面 app 放在根目錄，server 放在 `cmd/`。

```
ShareCodex/
├── main.go, desktop.go, scan.go   # 桌面 app（main package）；子指令 join、agent、scan、statusline
├── cmd/sharecodex-server/         # server 進入點（同步 API 與 /admin 管理介面）
├── internal/
│   ├── account/ identity/ usage/ quota/ attribution/   # domain，不依賴任何 adapter
│   ├── provider/
│   │   ├── lookpath.go            # 在 GUI app 精簡 PATH 下找 claude / codex CLI
│   │   ├── anthropic/
│   │   │   ├── transcript/        # transcript JSONL → usage.Event
│   │   │   ├── statusline/        # shim 與 spool → quota.Snapshot
│   │   │   ├── identity/          # claude auth status --json → 帳號觀測
│   │   │   └── config/            # statusLine 安裝器：Plan / Apply / Restore
│   │   └── openai/codex/
│   │       ├── rollout/           # token_usage_record、token_count、rate_limits
│   │       └── appserver/         # account/read、account/rateLimits/read
│   ├── scan/                      # 依 size/mtime 找出變動的 .jsonl
│   ├── atomicfile/                # 寫暫存檔再 rename
│   ├── syncapi/                   # client 與 server 共用的 wire DTO、路徑、協定版本
│   ├── client/
│   │   ├── agent/                 # 掃描、身分、同步三個迴圈與 popup 狀態
│   │   ├── storage/               # 本機 SQLite 與 outbox（migrations 以 embed 內嵌）
│   │   ├── sync/                  # 加入連結解析、pair、sync、overview HTTP client
│   │   ├── secret/                # 裝置 token 存 OS keychain
│   │   ├── settings/              # 資料目錄與設定檔
│   │   ├── autostart/             # 登入時啟動（macOS LaunchAgent、Windows Run key）
│   │   ├── update/                # 檢查 GitHub Release
│   │   └── desktop/               # Wails 系統匣、popup、bindings service
│   └── server/
│       ├── storage/               # Postgres（migrations 以 embed 內嵌）、冪等 ingest
│       ├── query/                 # overview：額度、分配、估計用量
│       ├── httpapi/               # /internal/api/v1 與 /join/
│       └── admin/                 # /admin 網頁管理介面（templates 以 embed 內嵌）
├── frontend/                      # Vite + Svelte 5 + TS；src/{components,features/{overview,settings},lib}
├── build/                         # Wails 建置設定、圖示、Info.plist、Windows 資源
├── testdata/{claude,codex}/       # 合成的 fixture（無真實 prompt）
├── deploy/                        # Dockerfile、docker-compose.yml（使用 GHCR 映像檔）、docker-compose.build.yml（從原始碼建置）、.env.example
├── .github/workflows/             # ci.yml、release.yml
└── docs/                          # plan.md、spikes.md
```

## Domain 模型重點

- **Account** `{ID, Provider(anthropic|openai), ExternalRefHash, Hint, Label, PlanType}`。
  - ExternalRef：Claude 用 `claude auth status --json` 的 `orgId`（缺少時用 `email`）；Codex 用 app-server `account/rateLimits/read` 的 `accountId`（缺少時用 `account/read` 的 `email`）。
  - Server 只存 `provider:ref` 的 SHA-256 雜湊，另存一個遮罩過的 `Hint`（例如 `k8***@privaterelay.appleid.com`）讓 admin 認得是哪個帳號。
  - **帳號自動建立**：Server 第一次收到某個 ref 的觀測時自動建立 Account，`Label` 預設為 Hint，admin 之後可以改名。因此不需要「未對應帳號」的指派流程。
- **Person 與 Device 分開。** Person 和 Account 是多對多（Membership），Membership 帶有 `ShareWeight`（見「額度占比分配」）。
- **帳號時間軸（Observation）。** agent 啟動時、每 5 分鐘、以及發現新 session 檔時，記錄 `{provider, ref_hash, observed_at}`。事件依 `occurred_at` 對到該時間點前最近一次的觀測（`account.ResolveAt`）。
  - **早於這台裝置第一次觀測的事件不會匯入。** 加入之前的歷史不屬於共用帳本，也不猜測它屬於哪個帳號。
  - 只接受訂閱登入：Claude 的 `authMethod` 必須是 `claude.ai`；Codex 的 account 型別必須是 `chatgpt`。用 API key 的用量不列入。
- **usage.Event**（只能新增，不修改）：`{DedupeKey, AccountRefHash, DeviceID, Provider, Product(claude-code|codex), Originator, SessionID, Model, OccurredAt, Tokens{Input, CachedInput, CacheWrite, Output, ReasoningOutput}}`。PersonID 由 Server 依裝置補上。
  - DedupeKey：Codex 用 `codex:<response_id>`，fallback 為 `codex:<session_id>:tc:<total_tokens>`；Claude 用 `claude:<message.id>:<requestId>`。
  - 絕不保存 prompt、cwd 或專案路徑。
- **quota.Snapshot**：`{AccountRefHash, DeviceID, Provider, ObservedAt, Source(codex-rollout|codex-rpc|claude-statusline), Buckets[]}`。每個 `Bucket` 是 `{Key(five_hour|weekly|…), UsedPercent?, ResetsAt?, WindowMinutes?}`。
  - 多台裝置回報衝突時，每個 bucket 取 `ObservedAt` 最新的一筆（`quota.Latest`）。
  - 如果 `ResetsAt` 已經過了，就視為歸零，顯示「已重置」。

## 成員加入流程

1. **管理者登入**：開啟 `PUBLIC_URL/admin/`，以 `.env` 的 `ADMIN_PASSWORD` 登入。管理權限來自這組密碼，不綁定成員身分，所以沒有「第一位 admin」的引導問題。
2. **建立成員並產生邀請**：在「成員」頁新增成員，按「產生加入連結」。
   - 頁面顯示一條加入連結 `https://<server>/join/<code>`，24 小時內有效、只能用一次。Server 只存 code 的雜湊，因此連結只顯示這一次。
3. **成員加入**：成員安裝桌面 app，貼上加入連結。app 會以 `POST /pair` 送出 `{code, device_name, platform}`，換回 `{device_id, person_id, token}`，token 存進 OS keychain。
4. **同一人的其他裝置**：對同一個 person 再產生一條邀請即可。一條邀請只能綁一台裝置。
5. **加入帳號**：不需要 admin 手動設定。成員在自己電腦上以共用帳號登入 Claude Code 或 Codex 後，裝置第一次回報該帳號的觀測，Server 就自動建立 Membership（`ShareWeight = 1`）。admin 可以在「帳號」頁調整權重；權重設為 0 即移出分配，但保留該成員的用量紀錄（Membership 列保留，自動加入不會把人加回來）。
6. **退出與撤銷**：在「裝置」頁撤銷裝置，讓 token 失效，之後該裝置的同步會收到 401，app 顯示「裝置已撤銷」。既有的用量紀錄保留，因為帳本只能新增。

## 額度占比分配

目標：每個共用帳號的每個額度視窗（5h、weekly），都能回答「每個人分到多少」和「每個人估計用了多少」。

- **分配（allotment）**：`allotted% = ShareWeight / Σ(該帳號所有成員的 ShareWeight) × 100`。
  - 預設所有人權重都是 1，也就是平均分配。
  - admin 可以在「帳號」頁調整，也能把尚未使用該帳號的成員先加進來。例如付比較多錢的人權重 2。
  - 分配只是資訊，**不強制執行**，因為技術上無法阻止某個人繼續使用共用帳號。
- **估計使用（usage estimate）**：對 bucket 的視窗 `[ResetsAt − WindowMinutes, now]`：
  - 每人的加權成本 `c_p = Σ price(model) · tokens`。價格表嵌入在 `internal/attribution/pricing.json`，只用來算相對比例，不代表實際帳單。
  - 每人的估計用量 `used%_p = UsedPercent × c_p / Σc`。
  - 如果視窗內有額度消耗但完全沒有事件（`Σc = 0`），整段 `UsedPercent` 列為 `unattributed`。
- **呈現**：每個人一條進度列，顯示「估計已用 x%」，並在 allotted 的位置標一條刻度線。
  - 狀態分成「未超出」和「超出分配」（`used% > allotted%`）。
  - 另外顯示「剩餘分配 = allotted% − used%」。
  - 所有個人數字都標示「估算」。帳號本身的 `UsedPercent` 是 provider 回報的實際值，不標示估算。
- **已知偏差**：成員加入前、同一視窗內的消耗，會按比例分攤給已經有事件的人。帳號的 `UsedPercent` 本身永遠正確。
- **模型用量**：同一視窗內依模型彙總請求數與 tokens（input、cached、cache write、output 合計），並以相同的加權成本比例估算每個模型占 `UsedPercent` 的多少。popup 與管理介面總覽都會顯示。

## 資料流細節

**檔案掃描**（`internal/scan`，兩個 provider 共用）：
- 記錄每個檔案的 `(path, size, mtime)`。檔案有變化時**整份重新解析**，再用 DedupeKey 寫入（output 變大才更新）。
- 這樣比 offset 增量解析簡單，也能正確處理 Codex fallback 需要的累計差額與 sub-agent 基準。實測單檔多在 1 MB 以下。
- 每 15 秒輪詢一次（只做 stat），開啟 popup 時立即掃描。不用 fsnotify：它不支援遞迴監看，而 Codex 每天建立新的日期目錄。

**Codex rollout**：
- 讀到 `session_meta` 時，如果 `model_provider != "openai"`，就略過整個檔案。
- 讀到 `turn_context` 時，更新目前使用的 model。
- 檔案有 `token_usage_record` 時，只用它轉成 usage.Event。
- 檔案沒有 `token_usage_record` 時（實測 399 個檔案中有 300 個，屬於**主要路徑**），改用 `token_count.info.total_token_usage` 累計值的差額。
  - 第一筆的基準是 `total − last_token_usage`，這樣可以排除 sub-agent 從父 session 繼承的累計值。
  - 累計值沒有增加的重複事件直接略過。
- `token_count.rate_limits` 在 primary 或 secondary 不為 null 時，轉成 quota.Snapshot。bucket key 依視窗長度決定：300 分鐘是 `five_hour`，10080 分鐘是 `weekly`，其他長度是 `window_<n>m`。
- 同時監看 `sessions` 與 `archived_sessions`（尊重 `CODEX_HOME`）。
- 每 5 分鐘透過 `codex app-server` 呼叫 `account/read` 與 `account/rateLimits/read`，取得身分觀測與主動額度快照（`codex-rpc`）。

**Claude transcript**：監看 `~/.claude/projects/**/*.jsonl`（尊重 `CLAUDE_CONFIG_DIR`）。
- 只收 `type:"assistant"` 且帶有 `message.usage` 的紀錄，而且模型必須符合 `^claude-(opus|sonnet|haiku)-`（排除 gateway 轉接的其他廠商模型與 `<synthetic>`）。
- 同一組 `message.id + requestId` 在同一檔或不同檔（resume）出現多次時只算一次，`output_tokens` 取最大值（串流中的前幾行是部分值）。本機與 Server 的寫入都採「output 變大才更新」。

**Claude statusLine shim**：做成桌面 app 同一個執行檔的子指令 `sharecodex statusline`，只發佈一個執行檔。
1. 從 stdin 讀取 JSON。
2. 用原子寫入（寫到暫存檔再 rename）把 `{session_id, rate_limits, observed_at}` 寫到 `<app data>/claude-statusline/<session_id>.json`。
3. 如果安裝前使用者已經有自己的 statusLine，就把同一份 stdin 轉給原本的指令，並原樣輸出結果；沒有的話輸出空字串。
4. 任何錯誤都不能影響 Claude Code。

**安裝器**（`provider/anthropic/config`）：讀取 `~/.claude/settings.json`，產生 ChangePlan，備份原檔後套用。原本的 statusLine 設定存到 `<app data>/statusline.json`，並提供 Restore。安裝器必須冪等：套用兩次時，第二次的 ChangePlan 應該是空的。

**Outbox**：
- 新事件、快照、觀測寫入本機的同一個 SQLite transaction 裡，同時寫入 outbox payload。
- sync worker 每 30 秒送出一批，最多 500 筆，失敗時用指數退避重試。
- 收到 Server 回應 200 後，刪除已送出的 outbox 項目。

**Server ingest**：在單一 transaction 內完成：
- 寫入觀測；如果帳號不存在就自動建立，並自動建立 Membership。
- 寫入事件（`ON CONFLICT (dedupe_key) DO NOTHING`，person_id 取自裝置）。
- 寫入快照。

協定版本（`syncapi.Version`）不符時回傳 426。

## Server API（`/internal/api/v1`，JSON）

| Method / Path | 用途 | 認證 |
|---|---|---|
| `POST /pair` | 用一次性邀請碼換取 device token | 邀請碼 |
| `POST /sync` | 批次上傳事件、快照、帳號觀測 | Device token |
| `POST /invite` | 替同一個人的另一台裝置產生一次性邀請碼，client 加上自己的 server URL 組成加入連結 | Device token |
| `GET /overview` | 所有帳號的額度 bucket，以及每人的分配%、估計用量%、各模型用量、unattributed、更新時間 | Device token |
| `GET /join/{code}` | 瀏覽器開啟加入連結時，顯示「請在 ShareCodex 桌面 app 貼上此連結」的純文字頁 | 無 |

- Device token 是 32 bytes 隨機值。Server 只存雜湊，client 存在 OS keychain；Linux 通常在沒有 keyring 的 SSH 環境執行，改存成僅限本人讀寫的檔案。
- 管理動作在 `/admin/` 網頁介面完成（Go `html/template` 伺服器端渲染，不另建 SPA）：
  - 以 `ADMIN_PASSWORD`（至少 12 字元）登入，session 存在記憶體（12 小時，重啟即失效）。登入失敗會延遲 1 秒並序列化，減慢暴力猜測。
  - Cookie 為 `HttpOnly`、`SameSite=Strict`，`PUBLIC_URL` 為 https 時加上 `Secure`；表單另以 Go 標準庫 `http.CrossOriginProtection` 擋跨站請求。
  - 頁面：總覽、成員（新增、產生加入連結）、帳號（命名、權重）、裝置（撤銷）。
  管理動作包括：person、invite、account label、share、device revoke。
- Router 使用 Go 標準庫 `net/http` 的 method routing，不引入 web 框架。

## 技術選擇（只列有實際理由的依賴）

| 用途 | 選擇 | 理由 |
|---|---|---|
| Client DB | `modernc.org/sqlite` | 純 Go，不需要 CGO，Windows 交叉編譯比較單純 |
| Server DB | `jackc/pgx/v5` | 直接寫 SQL，不使用 ORM |
| Migrations | `pressly/goose/v3`（embed） | client 與 server 共用同一套機制 |
| 桌面建置 | Wails 產生的 Taskfile（`wails3 task …`） | 沿用框架慣例處理 bindings、圖示、`.app` 打包 |
| 金鑰儲存 | `zalando/go-keyring` | macOS 用 Keychain，Windows 用 Credential Manager |
| 桌面 | `wailsapp/wails/v3`（鎖定確切版本） | 原生支援系統匣加 attached window |
| 前端 | Vite + Svelte 5 + TS，只做少量自製元件 | popup 很小，不需要 UI 框架 |
| 日誌 | 標準庫 `log/slog` | — |

## 里程碑（皆已完成）

- **M0 技術驗證**：結果見 `docs/spikes.md`。
- **M1 骨架與本機解析**：domain、兩個 provider 的解析器、`scan`、本機 SQLite、`sharecodex scan`、fixture 測試。
- **M2 額度與帳號時間軸**：Codex rollout 與 app-server 額度、statusLine shim 與安裝器、帳號觀測與事件歸屬。
- **M3 Server 與同步**：Postgres schema、`/admin` 網頁管理介面、pairing、冪等 `/sync`、outbox、`deploy/`。
- **M4 桌面 popup**：Wails v3 系統匣／選單列、Svelte popup、設定頁（加入、statusLine、登入時啟動）。
- **M5 歸因與 overview**：分配、估計用量、未歸屬、`/overview`。
- **M6 發佈**：CI（Go 三平台、Postgres 整合、前端檢查、桌面建置、server 映像檔建置）、release workflow（macOS universal、Windows、server binary、GHCR 多架構映像檔、Docker Compose bundle）、更新提示。
  macOS Developer ID 簽署與公證、Windows 程式碼簽章在設定對應 secrets 後才會執行，見 README。

## 錯誤處理原則

- 每個 provider 各自維護 `Health{Status, LastSuccess, LastError}`。解析失敗只讓該 provider 標記為 degraded，不影響其他部分；popup 會顯示最後更新時間和錯誤原因。
- 無法辨識的 JSONL 行會記錄下來並計數，然後跳過，**不會讓整個檔案失敗**。未知的 `type` 直接略過，因為格式常常新增欄位。
- Server 無法連線時，client 照常記錄到本機，outbox 會持續累積。

## 驗證方式

- **單元測試與 golden test：** `testdata/` 放去識別化的 fixture，並自動化檢查裡面沒有 prompt 內容。涵蓋以下情境：
  - 一般 session、cache 用量很大的 session、非 OpenAI provider（必須被忽略）、檔案被截斷後重讀、舊格式 fallback、sub-agent。
  - 執行 `go test ./...`。
- **ccusage 對照（開發時在本機執行，不放 CI）：** `sharecodex scan --json` 與 `bunx ccusage codex daily --json`、`bunx ccusage claude daily --json` 比對。已知差異見 `docs/spikes.md` 第 6 節。
- **冪等測試：**
  - 同一批資料送兩次，Server 筆數不變。
  - 安裝器套用兩次，第二次的 ChangePlan 是空的。
  - 同一個檔案重掃，事件數不變。
- **架構測試：** 在 `go test` 中用 `go list -deps` 檢查 domain package 沒有 import `database/sql`、`net/http`、`wails`。
- **端對端：**
  - 用 `docker compose up` 啟動 Server，建立帳號與邀請碼。
  - 兩台機器（或兩個 `SHARECODEX_HOME` profile）完成 pairing，分別執行 Codex 和 Claude Code。
  - 確認 popup 的額度與 Codex `/status`、Claude `/usage` 相符，而且兩個人的占比都出現。
- **前端：** `pnpm check`（svelte-check）與 `pnpm build`；以模擬資料在瀏覽器確認淺色、深色、未加入、超出分配等畫面。

## 第一版明確不做

ChatGPT 網頁用量、Gemini 或其他 provider、web 管理介面、額度預測與提醒推播、plugin 系統、微服務、Redis、Kafka、GraphQL、DI container。
