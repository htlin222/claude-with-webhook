# Contributing to claude-with-webhook

Thanks for your interest in contributing! This guide covers everything you need to get started.

## Development Environment Setup

### Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| **Go** | 1.23+ | Build the server (pure stdlib, no external deps) |
| **GitHub CLI** (`gh`) | Latest | Webhook management, PR creation, issue interaction |
| **Claude Code** (`claude`) | Latest | AI-powered issue implementation |
| **Tailscale** | With Funnel | Webhook ingress from GitHub to your local machine |
| **git** | Latest | Version control, worktree isolation |
| **jq** | Latest | JSON processing in scripts |
| **openssl** | Latest | Webhook secret generation |

The `gh` CLI must be authenticated with the `admin:repo_hook` scope for webhook creation:

```bash
gh auth login --scopes admin:repo_hook
```

### Environment Configuration

Copy `.env.example` to `.env` and configure:

```bash
GITHUB_WEBHOOK_SECRET=change-me    # HMAC-SHA256 secret for webhook verification
ALLOWED_USERS=htlin222             # Comma-separated GitHub usernames allowed to trigger automation
BOT_USERNAME=your-bot-username     # Prevents the bot from responding to its own comments
PORT=8080                          # Server listen port
MAX_CONCURRENT=3                   # Maximum concurrent Claude jobs (default: 3)
```

### Repository Registration

Register repos in `repos.conf`, one per line:

```
owner/repo=/local/path/to/repo
another-owner/repo=/another/local/path
```

The server reloads `repos.conf` on `SIGHUP` — no restart needed when adding repos.

## Building and Testing

### Build

```bash
go build -o claude-webhook-server .
```

### Run Tests

```bash
go test ./...
```

Tests cover: error sanitization (secret stripping, path redaction, truncation), dangerous file filtering, pattern matching, and log truncation.

### Start / Stop the Server

```bash
./start   # launches the server from ~/.claude-webhook/
./stop    # kills the running process
```

### Installation

- **Local install** (from source checkout): `./install.sh`
- **Remote install** (from any repo): `curl -sL https://raw.githubusercontent.com/htlin222/claude-with-webhook/main/remote-install.sh | bash`

## Code Style and Conventions

### Pure Stdlib

This project uses **only the Go standard library** — no external dependencies. Keep it that way.

### Structured Logging

Use `log.Printf` with the `[owner/repo#issue]` prefix pattern:

```go
log.Printf("[%s/%s#%d] Processing approve command", owner, repo, issueNum)
```

### Security First

These patterns are non-negotiable:

- **HMAC-SHA256 verification** on every incoming webhook
- **Allowed-users whitelist** — only configured users trigger automation
- **Bot self-filter** — `BOT_USERNAME` prevents self-triggering loops
- **Error sanitization** — strip secrets, redact paths, truncate output (see `sanitizeError()`)
- **Filtered git add** — dangerous files are never staged (see `filterSafeFiles()`)
- **Worktree isolation** — all work happens in `worktrees/issue-N`, never on `main`

### Concurrency

- **Semaphore** with configurable `MAX_CONCURRENT` limit — excess jobs are dropped with a warning comment
- **Per-issue mutex** via `sync.Map` — prevents race conditions on the same issue

### Timeouts

| Operation | Timeout |
|-----------|---------|
| Claude commands | 5 minutes |
| Git / gh commands | 30 seconds |

### Dangerous File Patterns

These are never staged by `filterSafeFiles()`:

- `.env*` — environment files
- `*.pem`, `*.key` — certificates and keys
- `*credential*`, `*secret*`, `*token*` — anything that smells like secrets
- `node_modules/` — dependencies
- `.git/` — git metadata

## Submitting Issues

- Use a clear, descriptive title
- Include reproduction steps if reporting a bug
- Provide relevant logs (sanitize secrets first!)
- The webhook will automatically trigger `@claude` to generate an implementation plan

## Submitting Pull Requests

### Branch Naming

```
issue-{number}
```

### Commit Message Format

```
Implement #{number}: {issue title}
```

### PR Title Format

```
Fix #{number}: {issue title}
```

### Rules

- **No force pushes** — keep history clean and reviewable
- PRs should reference the issue with `Closes #{number}` in the body
- One logical change per PR

## The @claude Webhook Workflow

When you interact with issues in this repo, the webhook server automates Claude-powered development:

### Automatic Planning

Opening a new issue automatically triggers Claude to generate an implementation plan. The plan is posted as a comment with an interactive menu.

### Commands

Use these commands in issue comments (must be the first line):

| Command | Effect |
|---------|--------|
| `@claude approve` | Approve the plan and start implementation |
| `@claude approve <guidance>` | Approve with additional instructions for Claude |
| `@claude lgtm` | Same as `@claude approve` |
| `@claude plan` | Regenerate the implementation plan |
| `@claude <question>` | Ask a follow-up question — Claude reads the full thread and responds |

### What Happens on Approve

1. Creates an isolated git worktree at `worktrees/issue-N`
2. Fetches `origin/main` for a fresh base
3. Runs Claude with streaming output (progress updates posted in real-time)
4. Stages only safe files (dangerous patterns filtered out)
5. Commits, pushes to `issue-N` branch, and opens a PR with `Closes #N`
6. Posts the PR link back in the issue

### Safety Checks

- Only users listed in `ALLOWED_USERS` can trigger automation
- The `@claude` prefix is required — prevents accidental triggers
- Concurrent job limits prevent resource exhaustion
- Per-issue locking prevents race conditions
- All work is isolated in worktrees — `main` is never modified directly
- Errors are sanitized before posting (secrets stripped, paths redacted)

### Streaming Progress

Claude's output streams to the issue comment in real-time:
- First update after **2 seconds** (quick feedback)
- Subsequent updates every **5 seconds** (respects GitHub rate limits)
- Final comment includes metadata: ⏱️ duration | 💰 cost | 🔄 turns
