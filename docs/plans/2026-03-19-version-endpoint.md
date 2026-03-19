# /version Endpoint Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `/version` endpoint that returns the server version and build time, injected via `-ldflags` at compile time.

**Architecture:** Define `version` and `buildTime` package-level variables with default values, override them via `go build -ldflags` in both install scripts. Register a `/version` handler alongside the existing `/health` handler.

**Tech Stack:** Go 1.23, `net/http`, `-ldflags` for build-time injection

---

### Task 1: Add version variables and handler

**Files:**
- Modify: `main.go` (add variables + handler)
- Test: `main_test.go` (add handler test)

**Step 1: Write the failing test**

Add to `main_test.go`:

```go
func TestVersionHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()

	handler := http.HandlerFunc(versionHandler)
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := resp["version"]; !ok {
		t.Error("response missing 'version' key")
	}
	if _, ok := resp["build_time"]; !ok {
		t.Error("response missing 'build_time' key")
	}
}
```

Add `"encoding/json"` and `"net/http"` and `"net/http/httptest"` to the test imports.

**Step 2: Run test to verify it fails**

Run: `go test -run TestVersionHandler -v`
Expected: FAIL — `versionHandler` undefined

**Step 3: Write minimal implementation**

Add to `main.go` near the top (after the `var` block):

```go
// Build-time variables, set via -ldflags.
var (
	version   = "dev"
	buildTime = "unknown"
)

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"version":    version,
		"build_time": buildTime,
	})
}
```

Register it in `main()` alongside the `/health` handler:

```go
mux.HandleFunc("/version", versionHandler)
```

**Step 4: Run test to verify it passes**

Run: `go test -run TestVersionHandler -v`
Expected: PASS

**Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add /version endpoint with build-time injection"
```

---

### Task 2: Wire ldflags into build scripts

**Files:**
- Modify: `install.sh:32` (add ldflags to `go build`)
- Modify: `remote-install.sh:55,64` (add ldflags to both `go build` lines)

**Step 1: Define ldflags in `install.sh`**

Replace the build line (line 32) with:

```bash
VERSION=$(git -C "$SERVER_DIR" describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-X main.version=$VERSION -X main.buildTime=$BUILD_TIME"
go build -C "$SERVER_DIR" -ldflags "$LDFLAGS" -o "$INSTALL_DIR/claude-webhook-server" .
```

**Step 2: Define ldflags in `remote-install.sh`**

Add before the build section (around line 53) and use in both build paths:

```bash
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-X main.version=$VERSION -X main.buildTime=$BUILD_TIME"
```

Local source build (line 55):
```bash
go build -C "$LOCAL_SRC" -ldflags "$LDFLAGS" -o "$SERVER_DIR/claude-webhook-server" .
```

Downloaded source build (line 64):
```bash
(cd "$SERVER_DIR" && go build -ldflags "$LDFLAGS" -o claude-webhook-server .)
```

**Step 3: Verify build locally**

Run: `go build -ldflags "-X main.version=test-v1 -X main.buildTime=2026-03-19" -o /tmp/test-server . && /tmp/test-server --help 2>&1; rm /tmp/test-server`

This just confirms the build succeeds (the server will fail to start without `.env`, which is expected).

**Step 4: Commit**

```bash
git add install.sh remote-install.sh
git commit -m "build: inject version and buildTime via ldflags"
```

---

### Task 3: Log version at startup

**Files:**
- Modify: `main.go` (add log line in `main()`)

**Step 1: Add startup log**

In `main()`, right after `cfg := loadConfig()`, add:

```go
log.Printf("claude-webhook-server %s (built %s)", version, buildTime)
```

**Step 2: Verify build and test**

Run: `go build . && go test ./... -v`
Expected: all tests pass, binary builds cleanly

**Step 3: Commit**

```bash
git add main.go
git commit -m "feat: log version at startup"
```
