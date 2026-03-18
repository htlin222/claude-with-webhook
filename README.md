# Claude Code Webhook Server

[繁體中文](README.zhtw.md)

A Go server that automates Claude Code planning and implementation via GitHub issues. When an allowed user opens an issue, Claude generates a plan. On approval, Claude implements the changes in a git worktree and opens a PR.

## Quick Install (for any repo)

```bash
cd /path/to/your-repo
curl -sL https://raw.githubusercontent.com/htlin222/claude-with-webhook/main/remote-install.sh | bash
```

This one command will:
- Download and build the server into `webhookd/`
- Auto-generate `.env` (webhook secret, GitHub user, available port)
- Add `webhookd/` to `.gitignore`
- Set up Tailscale Funnel
- Create the GitHub webhook

Then start it:

```bash
./webhookd/start
```

## Prerequisites

- [Go](https://go.dev/dl/) 1.23+
- [GitHub CLI](https://cli.github.com/) (`gh`) — authenticated
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`)
- [Tailscale](https://tailscale.com/download) with Funnel enabled
- Git, jq, openssl

## Manual Installation

If you prefer step-by-step setup or want to develop on this repo directly:

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
- Auto-generate `.env` (secret, GitHub user, port)
- Set up Tailscale Funnel and create the GitHub webhook

### 3. Start the server

```bash
./claude-webhook-server
```

Confirm it's running:

```bash
curl http://localhost:8080/claude-with-webhook/health
# {"status":"ok"}
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GITHUB_WEBHOOK_SECRET` | Secret used to verify webhook payloads from GitHub |
| `ALLOWED_USERS` | Comma-separated GitHub usernames allowed to trigger automation |
| `PORT` | Port the server listens on (default: `8080`) |

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
your-repo/
└── webhookd/              # Created by remote installer
    ├── claude-webhook-server  # Compiled binary
    ├── main.go                # Webhook server (pure stdlib)
    ├── go.mod                 # Go module (no external deps)
    ├── .env                   # Auto-generated config
    ├── .env.example           # Template
    ├── worktrees/             # Git worktrees for implementations
    ├── start                  # Start the server
    └── stop                   # Stop the server
```
