#!/usr/bin/env bash
set -euo pipefail

# Remote installer: curl -sL <url>/remote-install.sh | bash
# Run from any git repo root. Installs a centralized server at ~/.claude-webhook
# and registers this repo for webhook automation.

REPO_URL="https://raw.githubusercontent.com/htlin222/claude-with-webhook/main"
SERVER_DIR="$HOME/.claude-webhook"

echo "=== Claude Webhook Installer ==="
echo

# Must be in a git repo.
if ! git rev-parse --is-inside-work-tree &>/dev/null; then
  echo "ERROR: Not inside a git repository."
  exit 1
fi

REPO_DIR=$(git rev-parse --show-toplevel)

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

# --- Central server setup (only once) ---
mkdir -p "$SERVER_DIR"

echo "Downloading source..."
curl -sL "$REPO_URL/main.go"      -o "$SERVER_DIR/main.go"
curl -sL "$REPO_URL/go.mod"       -o "$SERVER_DIR/go.mod"
curl -sL "$REPO_URL/.env.example" -o "$SERVER_DIR/.env.example"

echo "Building server..."
(cd "$SERVER_DIR" && go build -o claude-webhook-server .)
echo "Built: $SERVER_DIR/claude-webhook-server"

# Generate .env if not present.
if [ -f "$SERVER_DIR/.env" ]; then
  echo ".env already exists, reusing."
else
  WEBHOOK_SECRET=$(openssl rand -hex 20)
  GH_USER=$(gh api user --jq '.login')
  PORT=8080

  cat > "$SERVER_DIR/.env" <<EOF
GITHUB_WEBHOOK_SECRET=$WEBHOOK_SECRET
ALLOWED_USERS=$GH_USER
PORT=$PORT
EOF
  echo "Generated .env (user=$GH_USER, port=$PORT)"
fi

# Source .env.
set -a
source "$SERVER_DIR/.env"
set +a

# --- Register this repo ---
REPOS_CONF="$SERVER_DIR/repos.conf"
touch "$REPOS_CONF"

GH_REPO=$(gh repo view --json nameWithOwner --jq '.nameWithOwner' 2>/dev/null || true)
if [ -z "$GH_REPO" ]; then
  echo "ERROR: Could not detect GitHub repo name."
  exit 1
fi

# Add or update repo entry.
if grep -q "^${GH_REPO}=" "$REPOS_CONF" 2>/dev/null; then
  # Update existing entry.
  sed -i.bak "s|^${GH_REPO}=.*|${GH_REPO}=${REPO_DIR}|" "$REPOS_CONF"
  rm -f "$REPOS_CONF.bak"
  echo "Updated repo: $GH_REPO → $REPO_DIR"
else
  echo "${GH_REPO}=${REPO_DIR}" >> "$REPOS_CONF"
  echo "Registered repo: $GH_REPO → $REPO_DIR"
fi

# Create worktrees dir in the repo.
mkdir -p "$REPO_DIR/worktrees"

# Add worktrees/ to .gitignore if not already there.
if ! grep -qx 'worktrees/' "$REPO_DIR/.gitignore" 2>/dev/null; then
  echo 'worktrees/' >> "$REPO_DIR/.gitignore"
  echo "Added worktrees/ to .gitignore"
fi

# --- Create start/stop scripts ---
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

# --- Ensure gh has admin:repo_hook scope ---
echo "Checking GitHub CLI scopes..."
SCOPES=$(gh auth status 2>&1 || true)
if echo "$SCOPES" | grep -q "admin:repo_hook"; then
  echo "admin:repo_hook scope OK."
else
  echo "Requesting admin:repo_hook scope..."
  gh auth refresh -h github.com -s admin:repo_hook
fi

# --- Tailscale Funnel (reuse if already running) ---
TS_HOSTNAME=$(tailscale status --json 2>/dev/null | jq -r '.Self.DNSName' | sed 's/\.$//')

FUNNEL_STATUS=$(tailscale funnel status 2>&1 || true)
if echo "$FUNNEL_STATUS" | grep -q "proxy http://127.0.0.1:${PORT}"; then
  echo "Tailscale Funnel already running on port $PORT."
else
  echo "Setting up Tailscale Funnel on port $PORT..."
  tailscale funnel --bg "$PORT"
fi

WEBHOOK_URL="https://${TS_HOSTNAME}/${GH_REPO}/webhook"
echo "Webhook URL: $WEBHOOK_URL"

# --- Create or update GitHub webhook ---
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

echo
echo "=== Installation Complete ==="
echo
echo "Repo registered: $GH_REPO → $REPO_DIR"
echo "Server dir:      $SERVER_DIR"
echo "Webhook URL:     $WEBHOOK_URL"
echo
echo "  Start:    $SERVER_DIR/start"
echo "  Stop:     $SERVER_DIR/stop"
echo "  Repos:    cat $SERVER_DIR/repos.conf"
echo "  Logs:     $SERVER_DIR/start 2>&1 | tee $SERVER_DIR/server.log"
echo
echo "Add more repos: cd /path/to/another-repo && curl -sL $REPO_URL/remote-install.sh | bash"
echo
