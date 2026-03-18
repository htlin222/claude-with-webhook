#!/usr/bin/env bash
set -euo pipefail

# Remote installer: curl -sL <url>/remote-install.sh | bash
# Run from the root of any git repo to install webhookd/ automation.

REPO_URL="https://raw.githubusercontent.com/htlin222/claude-with-webhook/main"

echo "=== Claude Webhook Installer ==="
echo

# Must be in a git repo.
if ! git rev-parse --is-inside-work-tree &>/dev/null; then
  echo "ERROR: Not inside a git repository."
  exit 1
fi

REPO_DIR=$(git rev-parse --show-toplevel)
WEBHOOKD="$REPO_DIR/webhookd"

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
echo "Installing into: $WEBHOOKD"

# Create webhookd directory.
mkdir -p "$WEBHOOKD"

# Download source files.
echo "Downloading source..."
curl -sL "$REPO_URL/main.go"      -o "$WEBHOOKD/main.go"
curl -sL "$REPO_URL/go.mod"       -o "$WEBHOOKD/go.mod"
curl -sL "$REPO_URL/.env.example" -o "$WEBHOOKD/.env.example"

# Build.
echo "Building server..."
(cd "$WEBHOOKD" && go build -o claude-webhook-server .)
echo "Built: $WEBHOOKD/claude-webhook-server"

# Generate .env if not present.
if [ -f "$WEBHOOKD/.env" ]; then
  echo ".env already exists, skipping."
else
  WEBHOOK_SECRET=$(openssl rand -hex 20)
  GH_USER=$(gh api user --jq '.login')
  # Find an available port (8080, 8081, 8082...).
  PORT=8080
  while lsof -i :"$PORT" &>/dev/null; do
    PORT=$((PORT + 1))
  done

  cat > "$WEBHOOKD/.env" <<EOF
GITHUB_WEBHOOK_SECRET=$WEBHOOK_SECRET
ALLOWED_USERS=$GH_USER
PORT=$PORT
EOF
  echo "Generated .env (user=$GH_USER, port=$PORT)"
fi

# Source .env.
set -a
source "$WEBHOOKD/.env"
set +a

# Create worktrees dir.
mkdir -p "$WEBHOOKD/worktrees"

# Add webhookd/ to .gitignore if not already there.
if ! grep -qx 'webhookd/' "$REPO_DIR/.gitignore" 2>/dev/null; then
  echo 'webhookd/' >> "$REPO_DIR/.gitignore"
  echo "Added webhookd/ to .gitignore"
fi

# Create start script.
cat > "$WEBHOOKD/start" <<'STARTEOF'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/.." && pwd)"
export REPO_ROOT
cd "$DIR"
exec ./claude-webhook-server "$@"
STARTEOF
chmod +x "$WEBHOOKD/start"

# Create stop script.
cat > "$WEBHOOKD/stop" <<'STOPEOF'
#!/usr/bin/env bash
pkill -f claude-webhook-server 2>/dev/null && echo "Server stopped." || echo "Server not running."
STOPEOF
chmod +x "$WEBHOOKD/stop"

# Ensure gh has admin:repo_hook scope.
echo "Checking GitHub CLI scopes..."
SCOPES=$(gh auth status 2>&1 || true)
if echo "$SCOPES" | grep -q "admin:repo_hook"; then
  echo "admin:repo_hook scope OK."
else
  echo "Requesting admin:repo_hook scope..."
  gh auth refresh -h github.com -s admin:repo_hook
fi

# Setup Tailscale Funnel.
echo "Setting up Tailscale Funnel on port $PORT..."
tailscale funnel --bg "$PORT"

TS_HOSTNAME=$(tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//')
WEBHOOK_URL="https://${TS_HOSTNAME}/claude-with-webhook/webhook"
echo "Webhook URL: $WEBHOOK_URL"

# Detect repo.
GH_REPO=$(gh repo view --json nameWithOwner --jq '.nameWithOwner' 2>/dev/null || true)
if [ -z "$GH_REPO" ]; then
  echo "WARNING: Could not detect GitHub repo. Set up webhook manually."
else
  # Create or update webhook.
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
echo "=== Installation Complete ==="
echo
echo "  Start:  ./webhookd/start"
echo "  Stop:   ./webhookd/stop"
echo "  Logs:   ./webhookd/start 2>&1 | tee webhookd/server.log"
echo
