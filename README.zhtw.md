# Claude Code Webhook Server

[English](README.md)

一個用 Go 撰寫的伺服器，透過 GitHub Issues 自動化 Claude Code 的規劃與實作流程。單一伺服器可處理多個 repo，透過 URL 路徑路由。當允許的使用者開啟 Issue 時，Claude 會自動產生計畫；經核准後，Claude 會在 git worktree 中實作變更並開啟 PR。

## 使用情境

團隊正在討論如何重構認證模組。五個人在 Issue 上留言 — 一個想用 OAuth，另一個偏好 JWT，有人提出向後相容的顧慮，PM 釐清了截止日期，一位 junior 工程師問「middleware 是什麼意思」。

通常，技術主管要讀完所有討論、寫出摘要、擬定計畫、寫程式碼、寫測試、開 PR、請人 review。半天就沒了。

有了這個專案，技術主管只需要打一行留言：

> `@claude approve 請彙整這個討論中每個人的意見，找出同時滿足安全性考量和截止日期的方案，並且實作含測試`

然後去吃午餐。

回來的時候，PR 已經在等了 — 程式碼寫好、測試通過、每位團隊成員的意見都反映在實作中。技術主管看看 diff，按下 merge，繼續下一件事。

**新的工作流程：** 人類討論。人類決策。Agent 執行剩下的一切。

唯一不可替代的是對話本身 — 點子、取捨、存在於每個人腦中的領域知識。之後的所有事情 — 整理、規劃、寫程式、測試、開 PR — 都是執行。而執行，正是 agent 的工作。

## 為什麼不用 [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions)？

Anthropic 提供了官方的 GitHub Actions 整合（[`anthropics/claude-code-action`](https://github.com/anthropics/claude-code-action)）。它是個很好的產品，但不適合我們的工作流程，所以自己造了一個。

| | GitHub Actions | 本專案（自架） |
|---|---|---|
| **執行環境** | GitHub 的 Ubuntu runner（每次觸發都要冷啟動） | 你自己的電腦（常駐運行） |
| **認證方式** | 需要 `ANTHROPIC_API_KEY`（API 計費） | 使用本機 `claude` CLI（Pro/Max/Team 方案） |
| **費用** | API token 費用 + GitHub Actions 分鐘數 | 你現有的訂閱，零額外費用 |
| **本地工具** | 沒有 — 沙箱環境，無法存取你的開發設定 | 完整存取 — 你的編輯器、linter、測試套件、資料庫 |
| **進度回饋** | 等整個 Action 跑完才看得到結果 | 即時串流 + 旋轉動畫 + 經過時間，每 2 秒更新 |
| **多 Repo** | 每個 repo 都要一個 workflow YAML | 一個伺服器，每個 repo 跑 `~/.claude-webhook/register` |
| **設定** | 安裝 GitHub App + 設定 API key + 複製 YAML | `make install` + `register`（不需要 API key） |
| **網路** | GitHub → Anthropic API | Tailscale Funnel、ngrok 或 zrok → localhost |

**總結：** 如果你已經有 Claude Code 訂閱，而且想用你本地的環境（工具、設定、測試基礎設施），這個專案就是為你設計的。如果你偏好完全託管、零基礎設施的方案且不介意 API 計費，官方的 GitHub Actions 是正確的選擇。

## 運作原理

```
你開 Issue ──→ GitHub 發送 webhook ──→ Tunnel (Tailscale/ngrok/zrok) ──→ 你的電腦
                                                                │
                     ┌─────────────────────────────────────────┘
                     ▼
              claude-webhook-server (localhost:8080)
                     │
                     ├─ 🤖 Planning… (立即發布進度留言)
                     ├─ Claude CLI 產生計畫（每 2 秒串流更新）
                     └─ 發布最終計畫，附上 @claude approve 指示
                                    │
               你留言               │
               "@claude approve" ───┘
                     │
                     ├─ 從 origin/main 建立 git worktree
                     ├─ Claude CLI 實作變更
                     ├─ 提交、推送、開啟 PR
                     └─ 更新進度留言附上 PR 連結
```

所有處理都在**你的電腦**上進行，使用**你本地的 `claude` CLI** — 不需要 API key，不需要雲端 runner。

## 前置需求

- [Go](https://go.dev/dl/) 1.23+
- [GitHub CLI](https://cli.github.com/) (`gh`) — 透過 `gh auth login` 完成認證
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`) — 需要有效訂閱
- [Tailscale](https://tailscale.com/download) 並啟用 [Funnel](https://tailscale.com/kb/1223/funnel)、[ngrok](https://ngrok.com/download) 或 [zrok](https://zrok.io)（擇一即可 — 用於建立通道）
- Git, jq, openssl

## 安裝

```bash
git clone https://github.com/htlin222/claude-with-webhook.git
cd claude-with-webhook
make install
```

建置執行檔並安裝所有內容到 `~/.claude-webhook/`，包含：
- 伺服器執行檔
- 用於新增 repo 的 `register` 腳本
- 啟動/停止腳本
- `.env` 設定檔（自動產生隨機 webhook 密鑰）

### 註冊 Repo

從任何想要自動化的 git repo 目錄執行：

```bash
cd /path/to/your-repo
~/.claude-webhook/register
```

**`register` 會依序執行以下步驟：**

1. 透過 `gh repo view` 偵測 GitHub repo 名稱
2. 將 repo 加入 `~/.claude-webhook/repos.conf`
3. 在 repo 內建立 `worktrees/` 目錄（自動加入 `.gitignore`）
4. 檢查 `gh` 是否有 `admin:repo_hook` 權限 — 若無，**會開啟瀏覽器**進行 OAuth 授權（僅需一次，用於建立 webhook）
5. 設定通道（Tailscale Funnel 或 ngrok）將流量導向你的本地連接埠
6. 建立（或更新）GitHub webhook，指向通道的公開 URL
7. 發送 SIGHUP 給運行中的伺服器，讓它立即載入新 repo

你可以註冊任意數量的 repo，每個都有自己的 webhook URL。

### 啟動伺服器

```bash
~/.claude-webhook/start
```

## 使用方式

### 建立計畫

在任何已註冊的 repo 開啟新 Issue，Claude 會分析內容並以留言方式發布計畫 — 工作期間會顯示帶有經過時間的進度指示器。

### 透過留言互動

所有指令都需要 `@claude` 前綴以避免意外觸發：

```
@claude approve                       # 開始實作
@claude approve focus on error handling and add tests
@claude approve 請用繁體中文寫註解
@claude lgtm                          # 等同 approve
@claude plan                          # 重新產生計畫（若 webhook 漏接）
@claude <追問問題>                     # 詢問任何問題
```

核准後，Claude 將會：

1. 從 `origin/main` 建立 git worktree 分支
2. 實作變更
3. 提交、推送並開啟 PR
4. 在 Issue 中留言附上 PR 連結

## 架構

```
~/.claude-webhook/              # 集中式伺服器（單一實例）
├── claude-webhook-server       # 執行檔
├── register                    # 註冊任何 repo（從 repo 目錄執行）
├── .env                        # 共用設定（密鑰、使用者、連接埠）
├── repos.conf                  # Repo 註冊檔
├── start / stop                # 控制腳本
└── source-repo                 # 原始碼 checkout 路徑

repos.conf:
  htlin222/repo-a=/Users/you/repo-a
  htlin222/repo-b=/Users/you/repo-b
```

Worktrees 建立在各 repo 內部：

```
/Users/you/repo-a/
└── worktrees/
    └── issue-3/                # Issue #3 的 git worktree
```

## 端點

每個已註冊的 repo 有自己的 webhook URL：

| 方法 | 路徑 | 說明 |
|------|------|------|
| `POST` | `/{owner}/{repo}/webhook` | 該 repo 的 webhook 接收端點 |
| `GET` | `/{owner}/{repo}/health` | 該 repo 的健康檢查 |
| `GET` | `/health` | 全域健康檢查 |
| `GET` | `/version` | 伺服器版本與建置時間 |

## 環境變數

| 變數 | 說明 |
|------|------|
| `GITHUB_WEBHOOK_SECRET` | 所有 repo webhook 共用的密鑰 |
| `ALLOWED_USERS` | 允許觸發自動化的 GitHub 使用者名稱（以逗號分隔） |
| `BOT_USERNAME` | 機器人的 GitHub 使用者名稱；會過濾自身留言以避免自我觸發 |
| `PORT` | 伺服器監聽的連接埠（預設：`8080`） |
| `MAX_CONCURRENT` | 最大同時處理任務數（預設：`3`） |

## 安全性

伺服器包含多項安全強化措施：

- **指令逾時** — 規劃：10 分鐘，追問：5 分鐘，實作：30 分鐘，git/gh 指令：30 秒（透過 `context.WithTimeout`）
- **並行限制** — 最多同時處理 3 個任務（可透過 `MAX_CONCURRENT` 設定）；超出時丟棄並記錄警告
- **錯誤清洗** — 發布到 GitHub 的錯誤留言會截斷至 500 字元，含有敏感關鍵字（`token`、`key`、`secret`、`password`、`credential`）的行會被移除，絕對路徑會被遮蔽
- **過濾式 git add** — 符合危險模式的檔案（`.env*`、`*.pem`、`*.key`、`*credential*`、`*secret*`、`*token*`、`node_modules/`、`.git/`）永遠不會被暫存或提交
- **Worktree 隔離** — 所有實作都在獨立的 git worktree 中執行，不影響主要 checkout

## 管理 Repo

```bash
# 列出已註冊的 repo
cat ~/.claude-webhook/repos.conf

# 新增 repo
cd /path/to/new-repo
~/.claude-webhook/register

# 更新原始碼後重新建置
cd /path/to/claude-with-webhook
make install

# 重啟伺服器
~/.claude-webhook/stop && ~/.claude-webhook/start
```

**建議：** 在 shell 設定檔（`~/.zshrc` 或 `~/.bashrc`）中加入別名：

```bash
alias cwh-register='~/.claude-webhook/register'
alias cwh-start='~/.claude-webhook/start'
alias cwh-stop='~/.claude-webhook/stop'
alias cwh-status='~/.claude-webhook/status'
```

## 常見問題

**Q: 需要 Anthropic API key 嗎？**
不需要。伺服器呼叫你本地的 `claude` CLI，使用你現有的 Claude Pro/Max/Team 訂閱。

**Q: 支援 Linux 嗎？**
支援。純 Go 實作，無作業系統相關程式碼。需要相同的前置需求（Go、gh、claude、tailscale/ngrok/zrok、git、jq、openssl）。

**Q: 多人可以共用一個伺服器嗎？**
可以 — 在 `.env` 的 `ALLOWED_USERS` 加入所有使用者名稱（以逗號分隔）。

**Q: 伺服器關閉時開了 Issue 怎麼辦？**
初始 webhook 會漏接。在 Issue 上留言 `@claude plan` 即可重新觸發規劃。

**Q: 為什麼 `register` 會開啟瀏覽器？**
建立 GitHub webhook 需要 `admin:repo_hook` OAuth 權限。只會發生一次 — 授權後，之後的 `register` 會跳過這個步驟。

**Q: Claude 實作錯了怎麼辦？**
關閉 PR，在 Issue 留下回饋，然後再次留言 `@claude approve` 並附上更具體的指示。Claude 會讀取完整討論串，包含你的回饋。

**Q: 可以用 ngrok 或 zrok 取代 Tailscale 嗎？**
可以。`register` 腳本會自動偵測可用的通道工具，依序檢查：`tailscale` → `ngrok` → `zrok`。如果你只安裝了 ngrok 或 zrok，它會自動啟動通道。注意 ngrok/zrok URL 每次重啟都會改變（除非你有付費方案的固定域名），所以重啟通道後需要重新執行 `register`。

**Q: 該選哪個通道工具？**
- **Tailscale Funnel** — 穩定的 HTTPS URL，綁定你的機器身份。不用擔心 tunnel 過期、不用管理 token。適合已經在用 Tailscale 的使用者。
- **ngrok** — 設定簡單（安裝後認證即可）。廣泛使用。免費方案 URL 會輪換；付費方案提供固定域名。
- **[zrok](https://zrok.io)** — 開源（基於 OpenZiti）。可自架，公開分享不需要帳號。適合想要完全掌控或避免供應商鎖定的使用者。

**Q: 哪些檔案永遠不會被提交？**
`.env*`、`*.pem`、`*.key`、`*credential*`、`*secret*`、`*token*`、`node_modules/`、`.git/` — 即使 Claude 嘗試暫存這些檔案，安全過濾器也會阻擋。

**Q: 如何解除安裝？**
`make uninstall` 會移除 `~/.claude-webhook/` 並停止伺服器。你可能也需要到 repo 設定頁面刪除 GitHub webhook。

## 授權

[MIT](LICENSE)
