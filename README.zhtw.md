# Claude Code Webhook Server

[English](README.md)

一個用 Go 撰寫的伺服器，透過 GitHub Issues 自動化 Claude Code 的規劃與實作流程。單一伺服器可處理多個 repo，透過 URL 路徑路由。當允許的使用者開啟 Issue 時，Claude 會自動產生計畫；經核准後，Claude 會在 git worktree 中實作變更並開啟 PR。

## 快速安裝

從任何想要自動化的 repo 目錄執行：

```bash
cd /path/to/your-repo
curl -sL https://raw.githubusercontent.com/htlin222/claude-with-webhook/main/remote-install.sh | bash
```

這會執行以下動作：
- 將伺服器安裝到 `~/.claude-webhook/`（所有 repo 共用）
- 將目前的 repo 註冊到 `repos.conf`
- 自動產生 `.env`（webhook 密鑰、GitHub 使用者、連接埠）
- 若已有 Tailscale Funnel 則直接複用，否則設定新的
- 為此 repo 建立 GitHub webhook

從不同的 repo 目錄重新執行即可新增更多 repo。

啟動伺服器：

```bash
~/.claude-webhook/start
```

## 前置需求

- [Go](https://go.dev/dl/) 1.23+
- [GitHub CLI](https://cli.github.com/) (`gh`) — 已完成認證
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`)
- [Tailscale](https://tailscale.com/download) 並啟用 Funnel
- Git, jq, openssl

## 使用方式

### 建立計畫

在任何已註冊的 repo 開啟新 Issue，Claude 會分析內容並以留言方式發布計畫。

### 追問問題

在 Issue 中留言 `@claude <你的問題>`，Claude 會讀取完整討論串後回覆。

### 核准實作

回覆 **Approve**（或 **lgtm**）開始實作。可以在同一行或後續行附加額外指示：

```
Approve
Approve focus on error handling and add tests
Approve 請用繁體中文寫註解

LGTM
用 TypeScript 並保持簡潔
```

Claude 將會：

1. 從 `origin/main` 建立 git worktree 分支
2. 實作變更
3. 提交、推送並開啟 PR
4. 在 Issue 中留言附上 PR 連結

## 架構

```
~/.claude-webhook/              # 集中式伺服器（單一實例）
├── claude-webhook-server       # 執行檔
├── main.go / go.mod            # 原始碼（純 stdlib，無外部依賴）
├── .env                        # 共用設定（密鑰、使用者、連接埠）
├── repos.conf                  # Repo 註冊檔
├── start / stop                # 控制腳本
└── .env.example
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

## 環境變數

| 變數 | 說明 |
|------|------|
| `GITHUB_WEBHOOK_SECRET` | 所有 repo webhook 共用的密鑰 |
| `ALLOWED_USERS` | 允許觸發自動化的 GitHub 使用者名稱（以逗號分隔） |
| `PORT` | 伺服器監聽的連接埠（預設：`8080`） |
| `MAX_CONCURRENT` | 最大同時處理任務數（預設：`3`） |

## 安全性

伺服器包含多項安全強化措施：

- **指令逾時** — Claude 指令：5 分鐘，git/gh 指令：30 秒（透過 `context.WithTimeout`）
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
curl -sL https://raw.githubusercontent.com/htlin222/claude-with-webhook/main/remote-install.sh | bash

# 重啟伺服器以載入新 repo
~/.claude-webhook/stop && ~/.claude-webhook/start
```
