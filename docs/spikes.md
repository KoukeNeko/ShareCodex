# M0 技術驗證結果

驗證環境：macOS、Codex CLI 0.154.0、Claude Code 2.1.281，2026-09-24。
只記錄欄位名稱與統計，不記錄帳號值或 prompt。

## 1. 帳號身分 — 成立（兩邊都能用官方介面取得）

- **Claude**：`claude auth status --json` 回傳 `loggedIn`、`authMethod`、`email`、`orgId`、`orgName`、`subscriptionType`。
  ExternalRef 用 `orgId`，缺少時用 `email`。
- **Codex**：`codex login status` 只印出登入方式，**沒有帳號識別**，不採用。
  改用 app-server 的 `account/read`，回傳 `ChatgptAccount{email, planType}`；
  `account/rateLimits/read` 的回應另有 `accountId`（backend 有提供時）。
  ExternalRef 優先用 `accountId`，否則用 `email`。

## 2. Codex app-server 額度 RPC — 成立

`codex app-server generate-json-schema` 確認有以下方法：
`initialize`、`account/read`、`account/rateLimits/read`、`account/usage/read`，
以及 server 通知 `AccountRateLimitsUpdatedNotification`。

`account/rateLimits/read` 的 `RateLimitSnapshot` 欄位：
`limitId`、`planType`、`primary`、`secondary`（各為 `{usedPercent, windowDurationMins, resetsAt}`）、
`credits{hasCredits, unlimited, balance}`、`rateLimitReachedType`。
背景輪詢時傳 `excludeResetCreditDetails: true`。

協定標示為 experimental，所以 M2b 的主動輪詢仍只當補充；被動讀取 rollout 內的 `rate_limits` 是主要來源。

## 3. Claude transcript — 成立

- 每筆 assistant 紀錄的頂層欄位包含 `requestId`、`sessionId`、`timestamp`、`isSidechain`、`message`。
- `message.usage` 包含 `input_tokens`、`output_tokens`、`cache_read_input_tokens`、`cache_creation_input_tokens`、`service_tier`、`speed`。
- **同一個回應會拆成多行**（每個 content block 一行）。實測 50 行 assistant 紀錄只有 21 組不同的
  `message.id + requestId`，而且同一組的 usage 完全相同。
  → 去重鍵 `claude:<message.id>:<requestId>`，取第一次出現的紀錄。
- `isSidechain` 可用來區分 sub-agent，但 sub-agent 本身就是實際消耗，照樣計入。

## 4. Codex 去重 — 成立，但 fallback 是主要路徑

- 全部 399 個 session 檔中，2,738 筆 `token_usage_record` 的 `response_id` 全部唯一，跨檔案也沒有重複。
- **只有 75 個檔案有 `token_usage_record`；300 個檔案只有 `token_count`**（多為 0.147 以前的版本）。
  所以 `token_count` 不是邊角情況，而是歷史資料的主要來源。
- Fallback 規則：以 `token_count.info.total_token_usage` 的累計值計算差額，
  去重鍵用 `codex:<session_id>:tc:<total_tokens>`。累計值在同一 session 內單調遞增，
  重複送出的相同 `token_count` 會自然被去重。
- 有 `token_usage_record` 的檔案只用它，不再讀 `token_count` 的用量，避免重複計算。
- 24 個檔案沒有任何用量紀錄，直接略過。
- Sub-agent 從父 session 繼承的累計值要當基準扣掉（ccusage 的做法），M1 用 fixture 驗證。

## 5. Wails v3 系統匣 — 成立

`v3.0.0-beta.25` 的 `SystemTray.AttachWindow` 可以直接把 frameless popup 掛在系統匣／選單列圖示上；
`ActivationPolicyAccessory` 加上 Info.plist 的 `LSUIElement` 讓 macOS 不顯示 Dock 圖示。
macOS 已實際建置、打包 `.app` 並啟動；Windows 以交叉編譯確認可建置，實機執行交給 CI 的 Windows runner 與人工測試。

## 6. 與 ccusage 的實測比對（本機 2026-09-15 ～ 09-24）

- **Codex**：每日的 input、cached、output 與 `ccusage codex daily` 完全一致。
  唯一差異在 09-23：ccusage 把 `model_provider = ollama-launch` 的 session 也算進去，我們依設計排除。
- **Claude**：修正以下兩點後，多數日期完全一致。
  1. **串流中的部分輸出**：同一個回應的多行紀錄中，前幾行的 `output_tokens` 是串流過程的部分值，
     最後一行才是最終值。實測一個專案有 5,380 組回應，取第一行合計 4.2M，取最大值合計 7.3M。
     → 同一個去重鍵取 **output 最大值**。本機與 Server 寫入時也要允許「用較大的 output 更新」，
     不能用單純的 `INSERT OR IGNORE`。
  2. **非 Anthropic 模型**：Claude Code 也能透過 gateway 使用 `deepseek-v4-pro`、`glm-5.3-flash`、
     `claude-ocx-ollama-cloud--…` 等模型。這些不消耗 Claude 訂閱額度，
     → 只收 `^claude-(opus|sonnet|haiku)-` 的模型。
- 剩下的差異來自 ccusage 本身**重複計算恢復（resume）的 session**：
  恢復的 session 會把先前的紀錄複製到新檔案。實測一個專案 221 個請求中有 88 個出現在 2～3 個檔案。
  ccusage 的 daily 報表把它們重複計算（例如 haiku 在 daily 是 20/382，在它自己的 session 報表是 10/191）。
  我們以 `message.id + requestId` 做全域去重，只算一次。
- **結論**：Claude 的 ccusage 比對只適合用在沒有 resume 的資料；CI 以我們自己的 fixture 與 golden test 為準。
  ccusage 也不讀我們合成的 Codex fixture（回傳空結果），所以 CI 不跑 ccusage oracle，
  改成開發時在本機執行 `sharecodex scan --json` 對照。
