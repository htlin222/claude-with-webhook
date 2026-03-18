#!/usr/bin/env bash
set -euo pipefail

echo "=== Claude Webhook Server Installer ==="
echo

# Check prerequisites.
missing=()
for cmd in go gh claude tailscale git jq; do
  if ! command -v "$cmd" &>/dev/null; then
    missing+=("$cmd")
  fi
done

if [ ${#missing[@]} -gt 0 ]; then
  echo "ERROR: Missing required tools: ${missing[*]}"
  echo "Please install them before continuing."
  exit 1
fi

echo "All prerequisites found."

# Build.
echo "Building server..."
go build -o claude-webhook-server .
echo "Built: ./claude-webhook-server"

# Create worktrees directory.
mkdir -p worktrees

# --- Generate .env automatically ---
if [ -f .env ]; then
  echo ".env already exists, skipping generation."
else
  echo "Generating .env..."

  # Generate webhook secret.
  WEBHOOK_SECRET=$(openssl rand -hex 20)

  # Get current GitHub username.
  GH_USER=$(gh api user --jq '.login')
  echo "Detected GitHub user: $GH_USER"

  # Detect port (default 8080).
  PORT=8080

  cat > .env <<EOF
GITHUB_WEBHOOK_SECRET=$WEBHOOK_SECRET
ALLOWED_USERS=$GH_USER
PORT=$PORT
EOF

  echo "Created .env (secret auto-generated, user=$GH_USER)"
fi

# Source .env for subsequent steps.
set -a
source .env
set +a

# --- Detect repo ---
REPO=$(gh repo view --json nameWithOwner --jq '.nameWithOwner' 2>/dev/null || true)
if [ -z "$REPO" ]; then
  echo "ERROR: Not inside a GitHub repo. Run this from the repo directory."
  exit 1
fi
echo "Repo: $REPO"

# --- Ensure gh has admin:repo_hook scope ---
echo "Checking GitHub CLI scopes..."
SCOPES=$(gh auth status 2>&1 || true)
if echo "$SCOPES" | grep -q "admin:repo_hook"; then
  echo "admin:repo_hook scope already granted."
else
  echo "Requesting admin:repo_hook scope (needed to manage webhooks)..."
  gh auth refresh -h github.com -s admin:repo_hook
fi

# --- Tailscale Funnel ---
echo "Setting up Tailscale Funnel on port $PORT..."
tailscale funnel --bg "$PORT"

TS_HOSTNAME=$(tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//')
WEBHOOK_URL="https://${TS_HOSTNAME}/claude-with-webhook/webhook"
echo "Webhook URL: $WEBHOOK_URL"

# --- Create GitHub webhook ---
echo "Checking for existing webhook..."
EXISTING_HOOK=$(gh api "repos/$REPO/hooks" --jq ".[] | select(.config.url == \"$WEBHOOK_URL\") | .id" 2>/dev/null || true)

if [ -n "$EXISTING_HOOK" ]; then
  echo "Webhook already exists (id=$EXISTING_HOOK), updating..."
  gh api "repos/$REPO/hooks/$EXISTING_HOOK" --method PATCH \
    -f "config[url]=$WEBHOOK_URL" \
    -f 'config[content_type]=json' \
    -f "config[secret]=$GITHUB_WEBHOOK_SECRET" \
    -f 'events[]=issues' \
    -f 'events[]=issue_comment' \
    -F active=true --silent
  echo "Webhook updated."
else
  echo "Creating GitHub webhook..."
  gh api "repos/$REPO/hooks" --method POST \
    -f name=web \
    -f "config[url]=$WEBHOOK_URL" \
    -f 'config[content_type]=json' \
    -f "config[secret]=$GITHUB_WEBHOOK_SECRET" \
    -f 'events[]=issues' \
    -f 'events[]=issue_comment' \
    -F active=true --silent
  echo "Webhook created."
fi

echo
echo "=== Setup Complete ==="
echo
echo "Webhook URL: $WEBHOOK_URL"
echo "Allowed users: $ALLOWED_USERS"
echo "Port: $PORT"
echo
echo "Start the server:"
echo "  ./claude-webhook-server"
echo
echo "Or run in background:"
echo "  nohup ./claude-webhook-server > /tmp/claude-webhook.log 2>&1 &"
echo
