#!/usr/bin/env bash
set -euo pipefail

# Local installer: run from the claude-with-webhook repo checkout.
# Builds server, registers this repo, sets up funnel + webhook.

SERVER_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== Claude Webhook Server Installer ==="
echo

# Check prerequisites.
missing=()
for cmd in go gh claude tailscale git jq openssl; do
  if ! command -v "$cmd" &>/dev/null; then
    missing+=("$cmd")
  fi
done

if [ ${#missing[@]} -gt 0 ]; then
  echo "ERROR: Missing required tools: ${missing[*]}"
  exit 1
fi

echo "All prerequisites found."

# Build and install to ~/.claude-webhook.
INSTALL_DIR="$HOME/.claude-webhook"
mkdir -p "$INSTALL_DIR"

echo "Building server..."
go build -C "$SERVER_DIR" -o "$INSTALL_DIR/claude-webhook-server" .
echo "$SERVER_DIR" > "$INSTALL_DIR/source-repo"
echo "Built: $INSTALL_DIR/claude-webhook-server (source: $SERVER_DIR)"

# Generate .env if not present.
if [ -f "$SERVER_DIR/.env" ]; then
  echo ".env already exists, reusing."
else
  WEBHOOK_SECRET=$(openssl rand -hex 20)
  GH_USER=$(gh api user --jq '.login')

  cat > "$SERVER_DIR/.env" <<EOF
GITHUB_WEBHOOK_SECRET=$WEBHOOK_SECRET
ALLOWED_USERS=$GH_USER
PORT=8080
EOF
  echo "Generated .env (user=$GH_USER)"
fi

set -a
source "$SERVER_DIR/.env"
set +a

# Register this repo in repos.conf.
REPOS_CONF="$SERVER_DIR/repos.conf"
touch "$REPOS_CONF"

GH_REPO=$(gh repo view --json nameWithOwner --jq '.nameWithOwner' 2>/dev/null || true)
if [ -z "$GH_REPO" ]; then
  echo "WARNING: Could not detect GitHub repo. Register repos manually in repos.conf."
else
  REPO_DIR=$(git rev-parse --show-toplevel 2>/dev/null || echo "$SERVER_DIR")

  if grep -q "^${GH_REPO}=" "$REPOS_CONF" 2>/dev/null; then
    sed -i.bak "s|^${GH_REPO}=.*|${GH_REPO}=${REPO_DIR}|" "$REPOS_CONF"
    rm -f "$REPOS_CONF.bak"
    echo "Updated repo: $GH_REPO → $REPO_DIR"
  else
    echo "${GH_REPO}=${REPO_DIR}" >> "$REPOS_CONF"
    echo "Registered repo: $GH_REPO → $REPO_DIR"
  fi

  mkdir -p "$REPO_DIR/worktrees"
fi

# Create start/stop scripts.
cat > "$SERVER_DIR/start" <<'STARTEOF'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"
exec ./claude-webhook-server "$@"
STARTEOF
chmod +x "$SERVER_DIR/start"

cat > "$SERVER_DIR/stop" <<'STOPEOF'
#!/usr/bin/env bash
pkill -f claude-webhook-server 2>/dev/null && echo "Server stopped." || echo "Server not running."
STOPEOF
chmod +x "$SERVER_DIR/stop"

# Ensure gh has admin:repo_hook scope.
echo "Checking GitHub CLI scopes..."
SCOPES=$(gh auth status 2>&1 || true)
if echo "$SCOPES" | grep -q "admin:repo_hook"; then
  echo "admin:repo_hook scope OK."
else
  echo "Requesting admin:repo_hook scope..."
  gh auth refresh -h github.com -s admin:repo_hook
fi

# Tailscale Funnel (reuse if already running).
TS_HOSTNAME=$(tailscale status --json 2>/dev/null | jq -r '.Self.DNSName' | sed 's/\.$//')

FUNNEL_STATUS=$(tailscale funnel status 2>&1 || true)
if echo "$FUNNEL_STATUS" | grep -q "proxy http://127.0.0.1:${PORT}"; then
  echo "Tailscale Funnel already running on port $PORT."
else
  echo "Setting up Tailscale Funnel on port $PORT..."
  tailscale funnel --bg "$PORT"
fi

# Create or update GitHub webhook.
if [ -n "${GH_REPO:-}" ]; then
  WEBHOOK_URL="https://${TS_HOSTNAME}/${GH_REPO}/webhook"
  echo "Webhook URL: $WEBHOOK_URL"

  EXISTING_HOOK=$(gh api "repos/$GH_REPO/hooks" --jq ".[] | select(.config.url == \"$WEBHOOK_URL\") | .id" 2>/dev/null || true)

  if [ -n "$EXISTING_HOOK" ]; then
    echo "Updating existing webhook..."
    gh api "repos/$GH_REPO/hooks/$EXISTING_HOOK" --method PATCH \
      -f "config[url]=$WEBHOOK_URL" \
      -f 'config[content_type]=json' \
      -f "config[secret]=$GITHUB_WEBHOOK_SECRET" \
      -f 'events[]=issues' \
      -f 'events[]=issue_comment' \
      -F active=true --silent
  else
    echo "Creating webhook..."
    gh api "repos/$GH_REPO/hooks" --method POST \
      -f name=web \
      -f "config[url]=$WEBHOOK_URL" \
      -f 'config[content_type]=json' \
      -f "config[secret]=$GITHUB_WEBHOOK_SECRET" \
      -f 'events[]=issues' \
      -f 'events[]=issue_comment' \
      -F active=true --silent
  fi
  echo "Webhook configured for $GH_REPO"
fi

echo
echo "=== Setup Complete ==="
echo
echo "Server dir:  $SERVER_DIR"
echo "Repos:       cat $SERVER_DIR/repos.conf"
echo
echo "  Start:  $SERVER_DIR/start"
echo "  Stop:   $SERVER_DIR/stop"
echo
