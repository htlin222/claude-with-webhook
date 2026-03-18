#!/usr/bin/env bash
set -euo pipefail

echo "=== Claude Webhook Server Installer ==="
echo

# Check prerequisites.
missing=()
for cmd in go gh claude tailscale git; do
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

# Protect existing .env.
if [ ! -f .env ]; then
  cp .env.example .env
  echo "Created .env from .env.example — edit it with your secrets."
else
  echo ".env already exists, skipping copy."
fi

# Create worktrees directory.
mkdir -p worktrees

echo
echo "=== Setup Complete ==="
echo
echo "Next steps:"
echo
echo "1. Edit .env with your GITHUB_WEBHOOK_SECRET"
echo
echo "2. Start the server:"
echo "   ./claude-webhook-server"
echo
echo "3. Expose via Tailscale Funnel:"
echo "   tailscale funnel --bg 8080"
echo
echo "4. Configure GitHub webhook:"
echo "   URL:    https://\$(tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//')/claude-with-webhook/webhook"
echo "   Secret: (value from .env)"
echo "   Events: Issues, Issue comments"
echo
