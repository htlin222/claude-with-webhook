# Claude Code Webhook Server

[![Stars](https://img.shields.io/github/stars/htlin222/claude-with-webhook?style=flat)](https://github.com/htlin222/claude-with-webhook/stargazers)
[![Last Commit](https://img.shields.io/github/last-commit/htlin222/claude-with-webhook)](https://github.com/htlin222/claude-with-webhook/commits/main)
[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[繁體中文](README.zhtw.md)

A Go server that automates Claude Code planning and implementation via GitHub issues. One server handles multiple repos, routed by URL path. When an allowed user opens an issue, Claude generates a plan. On approval, Claude implements the changes in a git worktree and opens a PR.

## Scenario

A team is debating how to refactor the authentication module. Five people leave comments on the issue — one wants OAuth, another prefers JWT, someone raises concerns about backward compatibility, the PM clarifies the deadline, and a junior dev asks what "middleware" means.

Normally, the tech lead reads all of this, writes a summary, drafts a plan, codes the solution, writes tests, opens a PR, and asks for review. That's half a day gone.

With this project, the tech lead types one comment:

> `@claude approve please gather everyone's opinions from this discussion, find the approach that satisfies the security concerns and the deadline, and implement it with tests`

Then goes to lunch.

By the time they're back, there's a PR waiting — code written, tests passing, every team member's concern addressed in the implementation. The tech lead reviews the diff, clicks merge, and moves on.

**The new workflow:** Humans discuss. Humans decide. The agent does the rest.

The only irreplaceable part is the conversation — the ideas, the tradeoffs, the domain knowledge that lives in people's heads. Everything after that — summarizing, planning, coding, testing, opening PRs — is execution. And execution is what agents are for.

## Why not [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions)?

Anthropic offers an official GitHub Actions integration ([`anthropics/claude-code-action`](https://github.com/anthropics/claude-code-action)). It's a solid product. But it didn't fit our workflow, so we built this instead.

| | GitHub Actions | This project (self-hosted) |
|---|---|---|
| **Runs on** | GitHub's Ubuntu runners (cold start every trigger) | Your own machine (always warm) |
| **Auth** | Requires `ANTHROPIC_API_KEY` (API billing) | Uses your local `claude` CLI (Pro/Max/Team plan) |
| **Cost** | API tokens + GitHub Actions minutes | Your existing subscription, zero extra |
| **Local tools** | None — sandbox environment, no access to your dev setup | Full access — your editors, linters, test suites, databases |
| **Progress feedback** | Wait for the entire Action to finish | Live streaming with animated SVG spinner, updates only on new output |
| **Multi-repo** | One workflow file per repo | One server, `~/.claude-webhook/register` per repo |
| **Setup** | Install GitHub App + add API key + copy YAML | `make install` + `register` (no API key needed) |
| **Networking** | GitHub → Anthropic API | Tailscale Funnel, ngrok, or zrok → localhost |

**TL;DR:** If you already have a Claude Code subscription and want to use your local environment (tools, configs, test infrastructure), this project lets you do that. If you prefer a managed, zero-infrastructure solution and don't mind API billing, the official GitHub Actions is the right choice.

## How it works

```
You open an Issue ──→ GitHub sends webhook ──→ Tunnel (Tailscale/ngrok/zrok) ──→ Your machine
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
- [Tailscale](https://tailscale.com/download) with [Funnel](https://tailscale.com/kb/1223/funnel) enabled, [ngrok](https://ngrok.com/download), or [zrok](https://zrok.io) (any one — for tunneling)
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

### Make commands

| Command | Description |
|---------|-------------|
| `make build` | Build the server binary in the current directory |
| `make install` | Build + install binary, scripts, and `.env` to `~/.claude-webhook/` |
| `make restart` | Build + install + stop running server + start new one |
| `make uninstall` | Stop the server and remove `~/.claude-webhook/` |
| `make clean` | Remove the local build artifact |

### Register a repo

From any git repo you want to automate:

```bash
cd /path/to/your-repo
~/.claude-webhook/register          # interactive — warns if another server exists
~/.claude-webhook/register --force  # auto-remove other server's webhook
```

**What `register` does step by step:**

1. Detects the GitHub repo name via `gh repo view`
2. Adds it to `~/.claude-webhook/repos.conf`
3. Creates a `worktrees/` directory in the repo (added to `.gitignore`)
4. Checks if `gh` has the `admin:repo_hook` scope — if not, **opens your browser** for OAuth consent (one-time, needed to create webhooks)
5. Sets up a tunnel (Tailscale Funnel or ngrok) to route traffic to your local port
6. Creates (or updates) a GitHub webhook pointing to your tunnel's public URL
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

These commands work on both **issues** and **pull requests**:

- **On an issue:** `@claude approve` creates a new branch, implements changes, and opens a PR.
- **On a PR:** `@claude approve` checks out the PR's existing branch, implements the requested changes, commits, and pushes directly to the PR branch.

On approve (from an issue), Claude will:

1. Create a git worktree branched from `origin/main`
2. Implement the changes
3. Commit, push, and open a PR
4. Comment on the issue with a link to the PR

On approve (from a PR), Claude will:

1. Fetch the PR branch and create a worktree tracking it
2. Read the full PR discussion (description + all comments)
3. Implement the requested changes
4. Commit and push to the PR branch

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
| `ALLOWED_USERS` | Comma-separated GitHub usernames always allowed (optional — repo collaborators with write+ permission are auto-allowed) |
| `BOT_USERNAME` | GitHub username of the bot; its own comments are filtered out to avoid self-triggering |
| `PORT` | Port the server listens on (default: `8080`) |
| `MAX_CONCURRENT` | Max concurrent jobs (default: `3`) |

## Dual-Account Setup (Primary + Bot)

### Why two accounts?

This project works best with two separate GitHub accounts:

- **Primary account** — your real account. You open issues, discuss, and comment `@claude approve`.
- **Bot account** — a secondary account authenticated on the VM. It posts plans, creates PRs, and pushes code.

This separation gives you:
- **Clarity** — you always know which comments are human vs automated
- **No infinite loops** — `BOT_USERNAME` filtering prevents the bot from triggering itself
- **Clean audit trail** — PRs and comments from the bot are visually distinct

### Setup on the VM

1. **Create a bot GitHub account** (e.g., `my-team-bot`) and add it as a collaborator to your repos (needs write access)

2. **On the VM, authenticate as the bot account:**
   ```bash
   gh auth login          # log in as the bot account
   claude                 # authenticate Claude Code (needs active subscription)
   ```

3. **Configure `.env`:**
   ```bash
   ALLOWED_USERS=your-primary-account
   BOT_USERNAME=my-team-bot
   ```

4. **Install and register:**
   ```bash
   make install
   cd /path/to/repo && ~/.claude-webhook/register
   ```

### How it looks

```
Primary account (you) ──→ opens issue / comments "@claude approve"
                              │
                              ▼ (webhook)
VM (authenticated as bot) ──→ claude-webhook-server
                              │
                              ├─ Claude Code generates plan
                              ├─ Bot account posts comment with plan
                              └─ Bot account opens PR with implementation
```

## Security

The server includes several hardening measures:

- **Command timeouts** — Planning: 30 min, follow-up: 30 min, implementation: 60 min, git/gh commands: 30 sec (via `context.WithTimeout`)
- **Concurrency limit** — Max 3 concurrent jobs (configurable via `MAX_CONCURRENT`); excess requests are dropped with a log warning
- **Event deduplication** — Each webhook delivery is tracked by its `X-GitHub-Delivery` UUID; duplicate events are silently skipped (cache auto-cleans hourly)
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
alias cwh-status='~/.claude-webhook/status'
```

## FAQ

**Q: Do I need an Anthropic API key?**
No. The server calls your local `claude` CLI, which uses your existing Claude Pro/Max/Team subscription.

**Q: Does it work on Linux?**
Yes. Pure Go with no OS-specific code. You need the same prerequisites (Go, gh, claude, tailscale/ngrok/zrok, git, jq, openssl).

**Q: Can multiple people share one server?**
Yes — add all usernames to `ALLOWED_USERS` in `.env` (comma-separated). Each user's comments will be processed if they match the list.

**Q: What happens if the server is down when an issue is opened?**
The initial webhook is missed. Comment `@claude plan` on the issue to re-trigger planning.

**Q: Why does `register` open my browser?**
It needs the `admin:repo_hook` OAuth scope to create GitHub webhooks. This only happens once — after granting the scope, future `register` calls skip this step.

**Q: What if Claude's implementation is wrong?**
Close the PR, leave feedback on the issue, and comment `@claude approve` again with more specific guidance. Claude reads the full discussion including your feedback.

**Q: Can I use ngrok or zrok instead of Tailscale?**
Yes. The `register` script auto-detects which tunnel tool is available. It checks in order: `tailscale` → `ngrok` → `zrok`. If you only have ngrok or zrok installed, it will start a tunnel automatically. Note that ngrok/zrok URLs change each time you restart (unless you have a paid plan with a static domain), so you'll need to re-run `register` after restarting the tunnel.

**Q: Which tunnel tool should I choose?**
- **Tailscale Funnel** — Stable HTTPS URL tied to your machine identity. No expiring tunnels, no token management. Best if you're already on Tailscale.
- **ngrok** — Easy to set up (install + authenticate). Widely used. Free tier has rotating URLs; paid plans offer static domains.
- **[zrok](https://zrok.io)** — Open-source (built on OpenZiti). Self-hostable, no account required for public shares. Good if you want full control or avoid vendor lock-in.

**Q: Can I run two servers for the same repo (e.g., on different machines)?**
No — this is intentionally a single-server-per-repo design. If two servers register webhooks for the same repo, GitHub delivers every event to both, causing duplicate plans and conflicting PRs. The `register` script detects this and warns you. If you're migrating a repo to a new machine, run `register --force` on the new machine to remove the old webhook automatically. As defense-in-depth, the server also deduplicates events using GitHub's `X-GitHub-Delivery` header.

**Q: What files are never committed?**
`.env*`, `*.pem`, `*.key`, `*credential*`, `*secret*`, `*token*`, `node_modules/`, `.git/` — the security filter blocks these even if Claude tries to stage them.

**Q: How do I uninstall?**
`make uninstall` removes `~/.claude-webhook/` and stops the server. You may also want to delete the GitHub webhooks via your repo settings.

## License

[MIT](LICENSE)
