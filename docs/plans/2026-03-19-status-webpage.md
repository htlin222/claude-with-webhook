# Status Webpage Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create a static HTML status page (`status.html`) that displays server health and version info by fetching `/health` and `/version` endpoints.

**Architecture:** A single self-contained HTML file with inline CSS and JavaScript. Served by the Go server via a new `/status` route using `http.HandleFunc`. The page fetches JSON from existing endpoints and renders status with auto-refresh.

**Tech Stack:** HTML5, vanilla CSS, vanilla JavaScript (fetch API), Go `net/http`

---

### Task 1: Add `/status` route serving the HTML file

**Files:**
- Create: `status.html` (the static page)
- Modify: `main.go:175-190` (add route after `/version`)
- Test: `main_test.go` (add handler test)

**Step 1: Write the failing test**

Add to `main_test.go`:

```go
func TestStatusEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "status.html")
	})

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatal("expected HTML content")
	}
	if !strings.Contains(body, "/health") {
		t.Fatal("expected page to reference /health endpoint")
	}
	if !strings.Contains(body, "/version") {
		t.Fatal("expected page to reference /version endpoint")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/htlin/claude-with-webhook && go test -run TestStatusEndpoint -v`
Expected: FAIL (status.html doesn't exist yet)

**Step 3: Create `status.html`**

Create `/Users/htlin/claude-with-webhook/status.html` with this content:

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Webhook Server Status</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace;
            background: #0d1117;
            color: #c9d1d9;
            display: flex;
            justify-content: center;
            padding: 2rem;
        }
        .container { max-width: 480px; width: 100%; }
        h1 {
            font-size: 1.25rem;
            margin-bottom: 1.5rem;
            color: #f0f6fc;
        }
        .card {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 6px;
            padding: 1rem 1.25rem;
            margin-bottom: 1rem;
        }
        .card-title {
            font-size: 0.75rem;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            color: #8b949e;
            margin-bottom: 0.5rem;
        }
        .card-value {
            font-size: 1rem;
            font-family: monospace;
        }
        .status-indicator {
            display: inline-block;
            width: 10px;
            height: 10px;
            border-radius: 50%;
            margin-right: 0.5rem;
            vertical-align: middle;
        }
        .status-ok { background: #3fb950; }
        .status-error { background: #f85149; }
        .status-loading { background: #8b949e; }
        .meta {
            font-size: 0.75rem;
            color: #484f58;
            margin-top: 1.5rem;
            text-align: center;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Webhook Server Status</h1>

        <div class="card">
            <div class="card-title">Health</div>
            <div class="card-value" id="health">
                <span class="status-indicator status-loading"></span>
                checking...
            </div>
        </div>

        <div class="card">
            <div class="card-title">Version</div>
            <div class="card-value" id="version">—</div>
        </div>

        <div class="card">
            <div class="card-title">Build Time</div>
            <div class="card-value" id="build-time">—</div>
        </div>

        <div class="meta" id="meta">Refreshing...</div>
    </div>

    <script>
        async function fetchStatus() {
            try {
                const healthRes = await fetch('/health');
                const health = await healthRes.json();
                const el = document.getElementById('health');
                if (health.status === 'ok') {
                    el.innerHTML = '<span class="status-indicator status-ok"></span>Healthy';
                } else {
                    el.innerHTML = '<span class="status-indicator status-error"></span>Unhealthy';
                }
            } catch {
                document.getElementById('health').innerHTML =
                    '<span class="status-indicator status-error"></span>Unreachable';
            }

            try {
                const versionRes = await fetch('/version');
                const ver = await versionRes.json();
                document.getElementById('version').textContent = ver.version || '—';
                document.getElementById('build-time').textContent = ver.build_time || '—';
            } catch {
                document.getElementById('version').textContent = 'unavailable';
                document.getElementById('build-time').textContent = 'unavailable';
            }

            document.getElementById('meta').textContent =
                'Last updated: ' + new Date().toLocaleTimeString();
        }

        fetchStatus();
        setInterval(fetchStatus, 30000);
    </script>
</body>
</html>
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/htlin/claude-with-webhook && go test -run TestStatusEndpoint -v`
Expected: PASS

**Step 5: Commit**

```bash
git add status.html main_test.go
git commit -m "test: add status.html and test for /status endpoint"
```

---

### Task 2: Wire `/status` route in `main.go`

**Files:**
- Modify: `main.go:190` (add handler after `/version` block)

**Step 1: Add the route**

After the `/version` handler (line 190), add:

```go
	// Status page.
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(baseDir, "status.html"))
	})
```

Note: `baseDir` is already computed in `loadConfig()` — verify it's accessible in `main()`. If not, use the same `os.Executable()` + `filepath.Dir()` pattern or pass it from `cfg`.

Check how `baseDir` is used — it's a field on the config struct. Access it as `cfg.BaseDir` or however the config exposes it. Read the config struct to confirm the field name.

**Step 2: Add `path/filepath` import if not already present**

Check imports at top of `main.go` — `path/filepath` is likely already imported for other path operations.

**Step 3: Run all tests**

Run: `cd /Users/htlin/claude-with-webhook && go test -v`
Expected: All tests pass

**Step 4: Manual smoke test**

Run: `cd /Users/htlin/claude-with-webhook && go run . &` then `curl -s http://localhost:8080/status | head -5`
Expected: Returns HTML starting with `<!DOCTYPE html>`
Kill the server after testing.

**Step 5: Commit**

```bash
git add main.go
git commit -m "feat: serve status.html at /status endpoint"
```

---

### Task 3: Run full test suite and verify

**Step 1: Run all existing tests**

Run: `cd /Users/htlin/claude-with-webhook && go test -v -count=1 ./...`
Expected: All tests pass

**Step 2: Verify the HTML file renders correctly**

Open `status.html` directly in a browser (file://) to verify styling. The fetch calls will fail (no server), but the layout should render correctly with "Unreachable" / "unavailable" states.

**Step 3: Final commit if any cleanup needed**

Only if changes were needed from verification.

---

## Summary

| Task | Description | Files |
|------|-------------|-------|
| 1 | Create `status.html` + test | `status.html` (new), `main_test.go` |
| 2 | Wire `/status` route | `main.go` |
| 3 | Full verification | — |

**Total: 3 tasks, ~2 new files, ~1 modified file**
