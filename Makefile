VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X main.version=$(VERSION) -X main.buildTime=$(BUILD)
INSTALL  := $(HOME)/.claude-webhook

.PHONY: build install uninstall clean restart

build:
	go build -ldflags "$(LDFLAGS)" -o claude-webhook-server .

install: build
	@mkdir -p $(INSTALL)
	@cp claude-webhook-server $(INSTALL)/
	@echo "$(CURDIR)" > $(INSTALL)/source-repo
	@# Install register script
	@cp scripts/register $(INSTALL)/register
	@chmod +x $(INSTALL)/register
	@# Install start/stop scripts
	@printf '#!/usr/bin/env bash\nset -euo pipefail\nDIR="$$(cd "$$(dirname "$$0")" && pwd)"\ncd "$$DIR"\nexec ./claude-webhook-server "$$@"\n' > $(INSTALL)/start
	@chmod +x $(INSTALL)/start
	@printf '#!/usr/bin/env bash\npkill -f claude-webhook-server 2>/dev/null && echo "Server stopped." || echo "Server not running."\n' > $(INSTALL)/stop
	@chmod +x $(INSTALL)/stop
	@# Generate .env if missing
	@if [ ! -f $(INSTALL)/.env ]; then \
		SECRET=$$(openssl rand -hex 20); \
		USER=$$(gh api user --jq '.login'); \
		printf 'GITHUB_WEBHOOK_SECRET=%s\nALLOWED_USERS=%s\nBOT_USERNAME=%s\nPORT=8080\n' "$$SECRET" "$$USER" "$$USER" > $(INSTALL)/.env; \
		echo "Generated .env (user=$$USER)"; \
	fi
	@echo
	@echo "Installed $(VERSION) → $(INSTALL)/claude-webhook-server"
	@echo
	@echo "  Start:     $(INSTALL)/start"
	@echo "  Stop:      $(INSTALL)/stop"
	@echo "  Register:  cd /path/to/repo && $(INSTALL)/register"
	@echo

restart: install
	@$(INSTALL)/stop 2>/dev/null || true
	@$(INSTALL)/start &
	@echo "Server restarted."

uninstall:
	@$(INSTALL)/stop 2>/dev/null || true
	@rm -rf $(INSTALL)
	@echo "Removed $(INSTALL)"

clean:
	@rm -f claude-webhook-server
