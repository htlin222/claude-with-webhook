# Claude Code Webhook Server

[English](README.md)

一個用 Go 撰寫的伺服器，透過 GitHub Issues 自動化 Claude Code 的規劃與實作流程。當允許的使用者開啟 Issue 時，Claude 會自動產生計畫；經核准後，Claude 會在 git worktree 中實作變更並開啟 PR。

## 前置需求

- [Go](https://go.dev/dl/) 1.23+
- [GitHub CLI](https://cli.github.com/) (`gh`) — 已完成認證
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`)
- [Tailscale](https://tailscale.com/download) 並啟用 Funnel
- Git

## 安裝

### 1. 複製儲存庫

```bash
git clone https://github.com/htlin222/claude-with-webhook.git
cd claude-with-webhook
```

### 2. 執行安裝腳本

```bash
bash install.sh
```

這會執行以下動作：
- 確認所有前置需求已安裝
- 編譯伺服器執行檔
- 從 `.env.example` 建立 `.env`（若 `.env` 尚不存在）
- 建立 `worktrees/` 目錄

### 3. 設定環境變數

編輯 `.env` 並填入你的設定值：

```bash
GITHUB_WEBHOOK_SECRET=your-secret-here
ALLOWED_USERS=htlin222
PORT=8080
```

| 變數 | 說明 |
|------|------|
| `GITHUB_WEBHOOK_SECRET` | 用於驗證 GitHub webhook 請求的密鑰 |
| `ALLOWED_USERS` | 允許觸發自動化的 GitHub 使用者名稱（以逗號分隔） |
| `PORT` | 伺服器監聽的連接埠（預設：`8080`） |

### 4. 啟動伺服器

```bash
./claude-webhook-server
```

確認伺服器正在運行：

```bash
curl http://localhost:8080/claude-with-webhook/health
# {"status":"ok"}
```

### 5. 透過 Tailscale Funnel 公開服務

```bash
tailscale funnel --bg 8080
```

取得你的公開 URL：

```bash
tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//'
```

你的 webhook URL 會是：

```
https://<your-tailscale-hostname>/claude-with-webhook/webhook
```

### 6. 設定 GitHub Webhook

1. 前往你的 GitHub 儲存庫 → **Settings** → **Webhooks** → **Add webhook**
2. **Payload URL**：`https://<your-tailscale-hostname>/claude-with-webhook/webhook`
3. **Content type**：`application/json`
4. **Secret**：與 `.env` 中的 `GITHUB_WEBHOOK_SECRET` 相同
5. **Events**：勾選 **Issues** 和 **Issue comments**
6. 點擊 **Add webhook**

## 使用方式

### 建立計畫

在儲存庫中開啟一個新的 Issue。如果你的 GitHub 使用者名稱在 `ALLOWED_USERS` 中，Claude 會分析 Issue 內容並以留言方式發布計畫。

### 核准實作

在 Issue 中回覆 **Approve**，Claude 將會：

1. 從 `origin/main` 建立一個 git worktree 分支
2. 實作變更
3. 提交、推送並開啟 PR
4. 在 Issue 中留言附上 PR 連結

## 端點

| 方法 | 路徑 | 說明 |
|------|------|------|
| `POST` | `/claude-with-webhook/webhook` | GitHub webhook 接收端點 |
| `GET` | `/claude-with-webhook/health` | 健康檢查 |

## 專案結構

```
claude-with-webhook/
├── main.go              # Webhook 伺服器（純 stdlib）
├── go.mod               # Go 模組（無外部依賴）
├── install.sh           # 安裝腳本
├── .env.example         # 環境變數範本
├── .env                 # 本地設定檔（已加入 git-ignore）
├── worktrees/           # 實作用的 Git worktrees（已加入 git-ignore）
└── README.md
```
