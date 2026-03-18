# Claude Code Webhook Server

[繁體中文](README.zhtw.md)

A Go server that automates Claude Code planning and implementation via GitHub issues. When an allowed user opens an issue, Claude generates a plan. On approval, Claude implements the changes in a git worktree and opens a PR.

## Prerequisites

- [Go](https://go.dev/dl/) 1.23+
- [GitHub CLI](https://cli.github.com/) (`gh`) — authenticated
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`)
- [Tailscale](https://tailscale.com/download) with Funnel enabled
- Git

## Installation

### 1. Clone the repo

```bash
git clone https://github.com/htlin222/claude-with-webhook.git
cd claude-with-webhook
```

### 2. Run the installer

```bash
bash install.sh
```

This will:
- Check that all prerequisites are installed
- Build the server binary
- Create `.env` from `.env.example` (if `.env` doesn't already exist)
- Create the `worktrees/` directory

### 3. Configure environment variables

Edit `.env` with your values:

```bash
GITHUB_WEBHOOK_SECRET=your-secret-here
ALLOWED_USERS=htlin222
PORT=8080
```

| Variable | Description |
|----------|-------------|
| `GITHUB_WEBHOOK_SECRET` | Secret used to verify webhook payloads from GitHub |
| `ALLOWED_USERS` | Comma-separated GitHub usernames allowed to trigger automation |
| `PORT` | Port the server listens on (default: `8080`) |

### 4. Start the server

```bash
./claude-webhook-server
```

Confirm it's running:

```bash
curl http://localhost:8080/claude-with-webhook/health
# {"status":"ok"}
```

### 5. Expose via Tailscale Funnel

```bash
tailscale funnel --bg 8080
```

Get your public URL:

```bash
tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//'
```

Your webhook URL will be:

```
https://<your-tailscale-hostname>/claude-with-webhook/webhook
```

### 6. Configure the GitHub webhook

1. Go to your repo on GitHub → **Settings** → **Webhooks** → **Add webhook**
2. **Payload URL**: `https://<your-tailscale-hostname>/claude-with-webhook/webhook`
3. **Content type**: `application/json`
4. **Secret**: same value as `GITHUB_WEBHOOK_SECRET` in `.env`
5. **Events**: select **Issues** and **Issue comments**
6. Click **Add webhook**

## Usage

### Create a plan

Open a new issue on the repo. If your GitHub username is in `ALLOWED_USERS`, Claude will analyze the issue and post a plan as a comment.

### Approve implementation

Reply **Approve** on the issue. Claude will:

1. Create a git worktree branched from `origin/main`
2. Implement the changes
3. Commit, push, and open a PR
4. Comment on the issue with a link to the PR

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/claude-with-webhook/webhook` | GitHub webhook receiver |
| `GET` | `/claude-with-webhook/health` | Health check |

## Project Structure

```
claude-with-webhook/
├── main.go              # Webhook server (pure stdlib)
├── go.mod               # Go module (no external deps)
├── install.sh           # Installer script
├── .env.example         # Template environment variables
├── .env                 # Your local config (git-ignored)
├── worktrees/           # Git worktrees for implementations (git-ignored)
└── README.md
```
