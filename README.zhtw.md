# Claude Code Webhook Server

[English](README.md)

一個用 Go 撰寫的伺服器，透過 GitHub Issues 自動化 Claude Code 的規劃與實作流程。單一伺服器可處理多個 repo，透過 URL 路徑路由。當允許的使用者開啟 Issue 時，Claude 會自動產生計畫；經核准後，Claude 會在 git worktree 中實作變更並開啟 PR。

## 安裝

```bash
git clone https://github.com/htlin222/claude-with-webhook.git
cd claude-with-webhook
make install
```

建置執行檔並安裝所有內容到 `~/.claude-webhook/`。

### 註冊 Repo

從任何想要自動化的 git repo 目錄執行：

```bash
cd /path/to/your-repo
~/.claude-webhook/register
```

這會註冊 repo、設定 Tailscale Funnel、並建立 GitHub webhook。

### 啟動伺服器

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

### 透過留言互動

所有指令都需要 `@claude` 前綴以避免意外觸發：

```
@claude approve
@claude approve focus on error handling and add tests
@claude approve 請用繁體中文寫註解
@claude lgtm
@claude plan                          # 重新產生計畫（若 webhook 漏接）
@claude <追問問題>
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
alias cwh-repos='cat ~/.claude-webhook/repos.conf'
```
