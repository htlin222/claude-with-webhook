# Claude Code Webhook Server

[繁體中文](README.zhtw.md)

A Go server that automates Claude Code planning and implementation via GitHub issues. One server handles multiple repos, routed by URL path. When an allowed user opens an issue, Claude generates a plan. On approval, Claude implements the changes in a git worktree and opens a PR.

## Quick Install

Run from any repo you want to automate:

```bash
cd /path/to/your-repo
curl -sL https://raw.githubusercontent.com/htlin222/claude-with-webhook/main/remote-install.sh | bash
```

This will:
- Install the server at `~/.claude-webhook/` (shared across all repos)
- Register the current repo in `repos.conf`
- Auto-generate `.env` (webhook secret, GitHub user, port)
- Reuse Tailscale Funnel if already running, or set one up
- Create the GitHub webhook for this repo

Add more repos by running the same command from each repo directory.

Start the server:

```bash
~/.claude-webhook/start
```

## Prerequisites

- [Go](https://go.dev/dl/) 1.23+
- [GitHub CLI](https://cli.github.com/) (`gh`) — authenticated
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`)
- [Tailscale](https://tailscale.com/download) with Funnel enabled
- Git, jq, openssl

## Usage

### Create a plan

Open a new issue on any registered repo. Claude will analyze the issue and post a plan as a comment.

### Ask follow-up questions

Comment `@claude <your question>` on the issue. Claude reads the full discussion and responds.

### Approve implementation

Comment **Approve** (or **lgtm**) to start. You can add extra instructions on the same line or as subsequent lines:

```
Approve
Approve focus on error handling and add tests
Approve 請用繁體中文寫註解

LGTM
use TypeScript and keep it simple
```

Claude will:

1. Create a git worktree branched from `origin/main`
2. Implement the changes
3. Commit, push, and open a PR
4. Comment on the issue with a link to the PR

## Architecture

```
~/.claude-webhook/              # Centralized server (one instance)
├── claude-webhook-server       # Binary
├── main.go / go.mod            # Source (pure stdlib, no deps)
├── .env                        # Shared config (secret, users, port)
├── repos.conf                  # Repo registry
├── start / stop                # Control scripts
└── .env.example

repos.conf:
  htlin222/repo-a=/Users/you/repo-a
  htlin222/repo-b=/Users/you/repo-b
```

Worktrees are created inside each repo:

```
/Users/you/repo-a/
└── worktrees/
    └── issue-3/                # Git worktree for issue #3
```

## Endpoints

Each registered repo gets its own webhook URL:

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/{owner}/{repo}/webhook` | Webhook receiver for that repo |
| `GET` | `/{owner}/{repo}/health` | Health check for that repo |
| `GET` | `/health` | Global health check |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GITHUB_WEBHOOK_SECRET` | Shared secret for all repo webhooks |
| `ALLOWED_USERS` | Comma-separated GitHub usernames allowed to trigger automation |
| `PORT` | Port the server listens on (default: `8080`) |
| `MAX_CONCURRENT` | Max concurrent jobs (default: `3`) |

## Security

The server includes several hardening measures:

- **Command timeouts** — Claude commands: 5 min, git/gh commands: 30 sec (via `context.WithTimeout`)
- **Concurrency limit** — Max 3 concurrent jobs (configurable via `MAX_CONCURRENT`); excess requests are dropped with a log warning
- **Error sanitization** — Error comments posted to GitHub are truncated to 500 chars, lines containing secret keywords (`token`, `key`, `secret`, `password`, `credential`) are stripped, and absolute file paths are redacted
- **Filtered git add** — Files matching dangerous patterns (`.env*`, `*.pem`, `*.key`, `*credential*`, `*secret*`, `*token*`, `node_modules/`, `.git/`) are never staged or committed
- **Worktree isolation** — All implementations run in isolated git worktrees, not the main checkout

## Managing Repos

```bash
# List registered repos
cat ~/.claude-webhook/repos.conf

# Add a new repo
cd /path/to/new-repo
curl -sL https://raw.githubusercontent.com/htlin222/claude-with-webhook/main/remote-install.sh | bash

# Restart server to pick up new repos
~/.claude-webhook/stop && ~/.claude-webhook/start
```
