# Claude Code Webhook Server

[繁體中文](README.zhtw.md)

A Go server that automates Claude Code planning and implementation via GitHub issues. One server handles multiple repos, routed by URL path. When an allowed user opens an issue, Claude generates a plan. On approval, Claude implements the changes in a git worktree and opens a PR.

## Why not [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions)?

Anthropic offers an official GitHub Actions integration ([`anthropics/claude-code-action`](https://github.com/anthropics/claude-code-action)). It's a solid product. But it didn't fit our workflow, so we built this instead.

| | GitHub Actions | This project (self-hosted) |
|---|---|---|
| **Runs on** | GitHub's Ubuntu runners (cold start every trigger) | Your own machine (always warm) |
| **Auth** | Requires `ANTHROPIC_API_KEY` (API billing) | Uses your local `claude` CLI (Pro/Max/Team plan) |
| **Cost** | API tokens + GitHub Actions minutes | Your existing subscription, zero extra |
| **Local tools** | None — sandbox environment, no access to your dev setup | Full access — your editors, linters, test suites, databases |
| **Progress feedback** | Wait for the entire Action to finish | Live streaming with spinner + elapsed time, updated every 2s |
| **Multi-repo** | One workflow file per repo | One server, `~/.claude-webhook/register` per repo |
| **Setup** | Install GitHub App + add API key + copy YAML | `make install` + `register` (no API key needed) |
| **Networking** | GitHub → Anthropic API | Tailscale Funnel → localhost |

**TL;DR:** If you already have a Claude Code subscription and want to use your local environment (tools, configs, test infrastructure), this project lets you do that. If you prefer a managed, zero-infrastructure solution and don't mind API billing, the official GitHub Actions is the right choice.

## How it works

```
You open an Issue ──→ GitHub sends webhook ──→ Tailscale Funnel ──→ Your machine
                                                                        │
                     ┌──────────────────────────────────────────────────┘
                     ▼
              claude-webhook-server (localhost:8080)
                     │
                     ├─ 🤖 Planning… (posts progress comment immediately)
                     ├─ Claude CLI generates a plan (streaming updates every 2s)
                     └─ Posts final plan with @claude approve instructions
                                    │
               You comment          │
               "@claude approve" ───┘
                     │
                     ├─ Creates git worktree from origin/main
                     ├─ Claude CLI implements the changes
                     ├─ Commits, pushes, opens a PR
                     └─ Updates the progress comment with PR link
```

All processing happens on **your machine** using **your local `claude` CLI** — no API key, no cloud runners.

## Prerequisites

- [Go](https://go.dev/dl/) 1.23+
- [GitHub CLI](https://cli.github.com/) (`gh`) — authenticated via `gh auth login`
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`) — with an active subscription
- [Tailscale](https://tailscale.com/download) with [Funnel](https://tailscale.com/kb/1223/funnel) enabled
- Git, jq, openssl

## Install

```bash
git clone https://github.com/htlin222/claude-with-webhook.git
cd claude-with-webhook
make install
```

This builds the binary and installs everything to `~/.claude-webhook/`, including:
- The server binary
- A `register` script for adding repos
- Start/stop scripts
- A `.env` config file (auto-generated with a random webhook secret)

### Register a repo

From any git repo you want to automate:

```bash
cd /path/to/your-repo
~/.claude-webhook/register
```

**What `register` does step by step:**

1. Detects the GitHub repo name via `gh repo view`
2. Adds it to `~/.claude-webhook/repos.conf`
3. Creates a `worktrees/` directory in the repo (added to `.gitignore`)
4. Checks if `gh` has the `admin:repo_hook` scope — if not, **opens your browser** for OAuth consent (one-time, needed to create webhooks)
5. Ensures Tailscale Funnel is routing traffic to your local port
6. Creates (or updates) a GitHub webhook pointing to `https://<your-tailscale-hostname>/<owner>/<repo>/webhook`
7. Sends SIGHUP to the running server so it picks up the new repo immediately

You can register as many repos as you want. Each one gets its own webhook URL.

### Start the server

```bash
~/.claude-webhook/start
```

## Usage

### Create a plan

Open a new issue on any registered repo. Claude will analyze the issue and post a plan as a comment — you'll see a progress indicator with elapsed time while it works.

### Interact via comments

All commands require the `@claude` prefix to prevent accidental triggers:

```
@claude approve                       # start implementation
@claude approve focus on error handling and add tests
@claude approve 請用繁體中文寫註解
@claude lgtm                          # same as approve
@claude plan                          # re-generate plan (if webhook was missed)
@claude <follow-up question>          # ask anything
```

On approve, Claude will:

1. Create a git worktree branched from `origin/main`
2. Implement the changes
3. Commit, push, and open a PR
4. Comment on the issue with a link to the PR

## Architecture

```
~/.claude-webhook/              # Centralized server (one instance)
├── claude-webhook-server       # Binary
├── register                    # Register any repo (run from repo dir)
├── .env                        # Shared config (secret, users, port)
├── repos.conf                  # Repo registry
├── start / stop                # Control scripts
└── source-repo                 # Path to source checkout

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
| `GET` | `/version` | Server version and build time |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GITHUB_WEBHOOK_SECRET` | Shared secret for all repo webhooks |
| `ALLOWED_USERS` | Comma-separated GitHub usernames allowed to trigger automation |
| `BOT_USERNAME` | GitHub username of the bot; its own comments are filtered out to avoid self-triggering |
| `PORT` | Port the server listens on (default: `8080`) |
| `MAX_CONCURRENT` | Max concurrent jobs (default: `3`) |

## Security

The server includes several hardening measures:

- **Command timeouts** — Planning: 10 min, follow-up: 5 min, implementation: 30 min, git/gh commands: 30 sec (via `context.WithTimeout`)
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
~/.claude-webhook/register

# Rebuild after source update
cd /path/to/claude-with-webhook
make install

# Restart server
~/.claude-webhook/stop && ~/.claude-webhook/start
```

**Tip:** Add aliases to your shell config (`~/.zshrc` or `~/.bashrc`):

```bash
alias cwh-register='~/.claude-webhook/register'
alias cwh-start='~/.claude-webhook/start'
alias cwh-stop='~/.claude-webhook/stop'
alias cwh-repos='cat ~/.claude-webhook/repos.conf'
```
