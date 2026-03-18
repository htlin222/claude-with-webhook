#!/usr/bin/env bash
set -euo pipefail

# Unregister a repo from the Claude Webhook server.
# Run from the repo you want to remove:
#   cd /path/to/your-repo
#   curl -sL <url>/remote-uninstall.sh | bash

SERVER_DIR="$HOME/.claude-webhook"
REPOS_CONF="$SERVER_DIR/repos.conf"

echo "=== Claude Webhook Uninstaller ==="
echo

if ! git rev-parse --is-inside-work-tree &>/dev/null; then
  echo "ERROR: Not inside a git repository."
  exit 1
fi

GH_REPO=$(gh repo view --json nameWithOwner --jq '.nameWithOwner' 2>/dev/null || true)
if [ -z "$GH_REPO" ]; then
  echo "ERROR: Could not detect GitHub repo name."
  exit 1
fi

echo "Repo: $GH_REPO"

# --- Remove from repos.conf ---
if [ -f "$REPOS_CONF" ] && grep -q "^${GH_REPO}=" "$REPOS_CONF"; then
  sed -i.bak "/^${GH_REPO}=/d" "$REPOS_CONF"
  rm -f "$REPOS_CONF.bak"
  echo "Removed from repos.conf"
else
  echo "Not found in repos.conf (already removed?)"
fi

# --- Delete GitHub webhook ---
if [ -f "$SERVER_DIR/.env" ]; then
  set -a
  source "$SERVER_DIR/.env"
  set +a

  TS_HOSTNAME=$(tailscale status --json 2>/dev/null | jq -r '.Self.DNSName' | sed 's/\.$//' || true)
  WEBHOOK_URL="https://${TS_HOSTNAME}/${GH_REPO}/webhook"

  HOOK_ID=$(gh api "repos/$GH_REPO/hooks" --jq ".[] | select(.config.url == \"$WEBHOOK_URL\") | .id" 2>/dev/null || true)
  if [ -n "$HOOK_ID" ]; then
    gh api "repos/$GH_REPO/hooks/$HOOK_ID" --method DELETE --silent
    echo "Deleted GitHub webhook (id: $HOOK_ID)"
  else
    echo "No matching webhook found on GitHub"
  fi
fi

# --- Clean up worktrees ---
REPO_DIR=$(git rev-parse --show-toplevel)
if [ -d "$REPO_DIR/worktrees" ]; then
  echo -n "Remove worktrees/ directory? [y/N] "
  read -r answer
  if [[ "$answer" =~ ^[Yy]$ ]]; then
    rm -rf "$REPO_DIR/worktrees"
    echo "Removed worktrees/"
  else
    echo "Kept worktrees/"
  fi
fi

echo
echo "=== Unregistered $GH_REPO ==="
echo "Restart the server to apply: $SERVER_DIR/stop && $SERVER_DIR/start"
echo
