package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		checks func(t *testing.T, result string)
	}{
		{
			name:  "strips lines with secret keywords",
			input: "normal line\nmy token is abc123\nanother line\npassword=hunter2",
			checks: func(t *testing.T, result string) {
				if strings.Contains(result, "token") {
					t.Error("should strip lines containing 'token'")
				}
				if strings.Contains(result, "password") {
					t.Error("should strip lines containing 'password'")
				}
				if !strings.Contains(result, "normal line") {
					t.Error("should keep normal lines")
				}
				if !strings.Contains(result, "another line") {
					t.Error("should keep non-secret lines")
				}
			},
		},
		{
			name:  "strips secret keyword case-insensitive",
			input: "MY_SECRET=foo\nSECRET_KEY=bar\nCredential: baz\nok line",
			checks: func(t *testing.T, result string) {
				if strings.Contains(result, "SECRET") || strings.Contains(result, "Credential") {
					t.Error("should strip secret lines case-insensitively")
				}
				if !strings.Contains(result, "ok line") {
					t.Error("should keep non-secret lines")
				}
			},
		},
		{
			name:  "redacts absolute paths",
			input: "error at /Users/htlin/projects/foo/main.go:42",
			checks: func(t *testing.T, result string) {
				if strings.Contains(result, "/Users/htlin") {
					t.Error("should redact /Users/... paths")
				}
				if !strings.Contains(result, "<redacted-path>/") {
					t.Error("should replace with <redacted-path>/")
				}
			},
		},
		{
			name:  "truncates long output",
			input: strings.Repeat("a", 1000),
			checks: func(t *testing.T, result string) {
				if len(result) > maxErrorLen+50 { // allow for "... (truncated)" suffix
					t.Errorf("should truncate, got length %d", len(result))
				}
				if !strings.Contains(result, "... (truncated)") {
					t.Error("should end with truncation marker")
				}
			},
		},
		{
			name:  "short output unchanged",
			input: "simple error",
			checks: func(t *testing.T, result string) {
				if result != "simple error" {
					t.Errorf("expected 'simple error', got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeError(tt.input)
			tt.checks(t, result)
		})
	}
}

func TestFilterSafeFiles(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		skipped  []string // files that should NOT appear
	}{
		{
			name:     "normal files pass through",
			input:    " M main.go\n M utils.go",
			expected: []string{"main.go", "utils.go"},
		},
		{
			name:     "filters .env files",
			input:    " M main.go\n?? .env\n?? .env.local",
			expected: []string{"main.go"},
			skipped:  []string{".env", ".env.local"},
		},
		{
			name:     "filters key and pem files",
			input:    " M main.go\n?? server.pem\n?? private.key",
			expected: []string{"main.go"},
			skipped:  []string{"server.pem", "private.key"},
		},
		{
			name:     "filters credential and secret files",
			input:    "?? credentials.json\n?? my_secret_file.txt\n?? token_cache.json\n M safe.go",
			expected: []string{"safe.go"},
			skipped:  []string{"credentials.json", "my_secret_file.txt", "token_cache.json"},
		},
		{
			name:     "filters node_modules",
			input:    " M main.go\n?? node_modules/foo/index.js",
			expected: []string{"main.go"},
			skipped:  []string{"node_modules/foo/index.js"},
		},
		{
			name:     "handles renamed files",
			input:    "R  old.go -> new.go",
			expected: []string{"new.go"},
		},
		{
			name:     "empty input returns nil",
			input:    "",
			expected: nil,
		},
		{
			name:     "all dangerous returns nil",
			input:    "?? .env\n?? secret.pem",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterSafeFiles(tt.input)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d files, got %d: %v", len(tt.expected), len(result), result)
			}
			for i, f := range tt.expected {
				if result[i] != f {
					t.Errorf("file[%d]: expected %q, got %q", i, f, result[i])
				}
			}

			for _, s := range tt.skipped {
				for _, r := range result {
					if r == s {
						t.Errorf("file %q should have been filtered out", s)
					}
				}
			}
		})
	}
}

func TestIsDangerousFile(t *testing.T) {
	dangerous := []string{
		".env",
		".env.local",
		"server.pem",
		"private.key",
		"credentials.json",
		"my_secret_config.yaml",
		"auth_token_cache",
		"node_modules/pkg/index.js",
		".git/config",
	}

	safe := []string{
		"main.go",
		"README.md",
		"src/handler.go",
		"package.json",
		"Dockerfile",
		".gitignore",
	}

	for _, f := range dangerous {
		t.Run("dangerous:"+f, func(t *testing.T) {
			if !isDangerousFile(f) {
				t.Errorf("%q should be dangerous", f)
			}
		})
	}

	for _, f := range safe {
		t.Run("safe:"+f, func(t *testing.T) {
			if isDangerousFile(f) {
				t.Errorf("%q should be safe", f)
			}
		})
	}
}

func TestClassifyComment(t *testing.T) {
	baseCfg := &Config{
		AllowedUsers: map[string]bool{"alice": true},
		BotUsername:   "my-bot",
	}

	tests := []struct {
		name       string
		cfg        *Config
		sender     string
		senderType string
		body       string
		expected   string
	}{
		// BOT_USERNAME / sender filtering
		{
			name:       "Bot type filtered",
			cfg:        baseCfg,
			sender:     "ci-bot",
			senderType: "Bot",
			body:       "@claude approve",
			expected:   "skip-bot",
		},
		{
			name:       "BOT_USERNAME filtered",
			cfg:        baseCfg,
			sender:     "my-bot",
			senderType: "User",
			body:       "@claude approve",
			expected:   "skip-self",
		},
		{
			name:       "BOT_USERNAME case mismatch filtered",
			cfg:        baseCfg,
			sender:     "My-Bot",
			senderType: "User",
			body:       "@claude approve",
			expected:   "skip-self",
		},
		{
			name:       "allowed user passes",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude approve",
			expected:   "approve",
		},
		{
			name: "no BOT_USERNAME set, allowed user passes",
			cfg: &Config{
				AllowedUsers: map[string]bool{"alice": true},
				BotUsername:   "",
			},
			sender:     "alice",
			senderType: "User",
			body:       "@claude approve",
			expected:   "approve",
		},
		{
			name:       "non-allowed user skipped",
			cfg:        baseCfg,
			sender:     "mallory",
			senderType: "User",
			body:       "@claude approve",
			expected:   "skip-user",
		},

		// Command routing
		{
			name:       "approve command",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude approve",
			expected:   "approve",
		},
		{
			name:       "approved command",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude approved",
			expected:   "approve",
		},
		{
			name:       "lgtm command",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude lgtm",
			expected:   "approve",
		},
		{
			name:       "approve with inline guidance",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude approve focus on error handling",
			expected:   "approve",
		},
		{
			name:       "plan command",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude plan",
			expected:   "plan",
		},
		{
			name:       "bare mention skipped",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude",
			expected:   "skip-bare-mention",
		},
		{
			name:       "follow-up question",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "@claude what about error handling?",
			expected:   "followup",
		},
		{
			name:       "no @claude prefix",
			cfg:        baseCfg,
			sender:     "alice",
			senderType: "User",
			body:       "this is a regular comment",
			expected:   "skip-no-prefix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyComment(tt.cfg, tt.sender, tt.senderType, tt.body)
			if got != tt.expected {
				t.Errorf("classifyComment() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestExactApproveMatching(t *testing.T) {
	cfg := &Config{
		AllowedUsers: map[string]bool{"alice": true},
	}

	tests := []struct {
		body     string
		expected string
	}{
		{"@claude approve", "approve"},
		{"@claude approved", "approve"},
		{"@claude lgtm", "approve"},
		{"@claude approve focus on tests", "approve"},
		// These should NOT trigger approve — they are follow-ups
		{"@claude I approve of this approach", "followup"},
		{"@claude the plan looks approved already", "followup"},
		{"@claude approving this seems premature", "followup"},
	}

	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			got := classifyComment(cfg, "alice", "User", tt.body)
			if got != tt.expected {
				t.Errorf("classifyComment(%q) = %q, want %q", tt.body, got, tt.expected)
			}
		})
	}
}

func TestWebhookSignatureVerification(t *testing.T) {
	secret := "test-secret-123"
	cfg := &Config{
		WebhookSecret: secret,
		AllowedUsers:  map[string]bool{"alice": true},
		repos:         map[string]string{"owner/repo": "/tmp/repo"},
		Port:          "0",
	}

	// Initialize semaphore for the handler.
	semaphore = make(chan struct{}, 3)

	validPayload := `{"action":"ping","repository":{"full_name":"owner/repo"}}`

	sign := func(payload string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(payload))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}

	tests := []struct {
		name       string
		signature  string
		body       string
		wantStatus int
	}{
		{
			name:       "valid signature accepted",
			signature:  sign(validPayload),
			body:       validPayload,
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing signature rejected",
			signature:  "",
			body:       validPayload,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid signature rejected",
			signature:  "sha256=0000000000000000000000000000000000000000000000000000000000000000",
			body:       validPayload,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed signature rejected",
			signature:  "not-a-valid-sig",
			body:       validPayload,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/owner/repo/webhook", strings.NewReader(tt.body))
			req.Header.Set("X-GitHub-Event", "ping")
			if tt.signature != "" {
				req.Header.Set("X-Hub-Signature-256", tt.signature)
			}

			rr := httptest.NewRecorder()
			handleWebhook(rr, req, cfg, "owner/repo")

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}

func TestQueuedConcurrency(t *testing.T) {
	t.Run("semaphore_blocks_then_proceeds", func(t *testing.T) {
		// Create a semaphore with capacity 1.
		semaphore = make(chan struct{}, 1)

		// Fill the single slot.
		semaphore <- struct{}{}

		acquired := make(chan struct{})
		go func() {
			semaphore <- struct{}{} // should block until slot freed
			close(acquired)
		}()

		// Verify it blocks: should NOT acquire within 50ms.
		select {
		case <-acquired:
			t.Fatal("goroutine should have blocked on full semaphore")
		case <-time.After(50 * time.Millisecond):
			// expected: still blocked
		}

		// Free one slot.
		<-semaphore

		// Verify it proceeds: should acquire within 100ms.
		select {
		case <-acquired:
			// expected: unblocked
		case <-time.After(100 * time.Millisecond):
			t.Fatal("goroutine should have acquired semaphore after release")
		}

		// Drain the slot the goroutine filled.
		<-semaphore
	})

	t.Run("per_issue_mutex_serializes", func(t *testing.T) {
		var localIssueMu sync.Map
		lockKey := "owner/repo#42"

		mu, _ := localIssueMu.LoadOrStore(lockKey, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()

		acquired := make(chan struct{})
		go func() {
			m, _ := localIssueMu.LoadOrStore(lockKey, &sync.Mutex{})
			m.(*sync.Mutex).Lock()
			close(acquired)
			m.(*sync.Mutex).Unlock()
		}()

		// Verify it blocks.
		select {
		case <-acquired:
			t.Fatal("goroutine should have blocked on locked mutex")
		case <-time.After(50 * time.Millisecond):
			// expected: still blocked
		}

		// Unlock and verify it proceeds.
		mu.(*sync.Mutex).Unlock()

		select {
		case <-acquired:
			// expected: unblocked
		case <-time.After(100 * time.Millisecond):
			t.Fatal("goroutine should have acquired mutex after unlock")
		}
	})
}

func TestSpinnerSVG(t *testing.T) {
	t.Run("spinnerImg_contains_markdown_image", func(t *testing.T) {
		if !strings.Contains(spinnerImg, "![](https://raw.githubusercontent.com/") {
			t.Error("spinnerImg should contain markdown image syntax with GitHub raw URL")
		}
		if !strings.Contains(spinnerImg, `<div align="center">`) {
			t.Error("spinnerImg should contain centered div wrapper")
		}
		if !strings.Contains(spinnerImg, "spinner.svg") {
			t.Error("spinnerImg should reference spinner.svg")
		}
	})

	t.Run("progressBody_empty_partial", func(t *testing.T) {
		body := progressBody("Planning", "")
		if !strings.Contains(body, spinnerImg) {
			t.Error("progressBody with empty partial should contain spinnerImg")
		}
		if !strings.Contains(body, "🤖 Planning") {
			t.Error("progressBody should contain action header")
		}
		// Should be just header, no trailing content.
		expected := "🤖 Planning\n\n" + spinnerImg
		if body != expected {
			t.Errorf("progressBody empty partial:\ngot:  %q\nwant: %q", body, expected)
		}
	})

	t.Run("progressBody_with_content", func(t *testing.T) {
		body := progressBody("Planning", "some output")
		if !strings.Contains(body, spinnerImg) {
			t.Error("progressBody should contain spinnerImg")
		}
		if !strings.Contains(body, "some output") {
			t.Error("progressBody should contain partial text")
		}
	})
}

func TestProgressUpdateDedup(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	var accumulated strings.Builder
	var accMu sync.Mutex
	var lastPartial string

	ticker := time.NewTicker(10 * time.Millisecond)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				accMu.Lock()
				partial := accumulated.String()
				accMu.Unlock()
				if partial == lastPartial {
					continue
				}
				lastPartial = partial
				mu.Lock()
				calls = append(calls, partial)
				mu.Unlock()
			case <-done:
				return
			}
		}
	}()

	// Write "hello", wait for tick to fire.
	accMu.Lock()
	accumulated.WriteString("hello")
	accMu.Unlock()
	time.Sleep(30 * time.Millisecond)

	// No change — tick should skip.
	time.Sleep(30 * time.Millisecond)

	// Write " world" — tick should fire again.
	accMu.Lock()
	accumulated.WriteString(" world")
	accMu.Unlock()
	time.Sleep(30 * time.Millisecond)

	close(done)
	ticker.Stop()

	mu.Lock()
	defer mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 update calls, got %d: %v", len(calls), calls)
	}
	if calls[0] != "hello" {
		t.Errorf("calls[0] = %q, want %q", calls[0], "hello")
	}
	if calls[1] != "hello world" {
		t.Errorf("calls[1] = %q, want %q", calls[1], "hello world")
	}
}

func TestPRDetection(t *testing.T) {
	t.Run("issue_comment_has_no_pull_request", func(t *testing.T) {
		payload := `{"action":"created","issue":{"number":1,"title":"test","body":"","user":{"login":"alice"}},"comment":{"id":1,"body":"@claude approve","user":{"login":"alice"}},"sender":{"login":"alice","type":"User"},"repository":{"full_name":"owner/repo"}}`
		var p webhookPayload
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.Issue.PullRequest != nil {
			t.Error("issue comment should have nil PullRequest")
		}
	})

	t.Run("pr_comment_has_pull_request", func(t *testing.T) {
		payload := `{"action":"created","issue":{"number":5,"title":"Fix bug","body":"","user":{"login":"alice"},"pull_request":{"url":"https://api.github.com/repos/owner/repo/pulls/5"}},"comment":{"id":2,"body":"@claude approve","user":{"login":"alice"}},"sender":{"login":"alice","type":"User"},"repository":{"full_name":"owner/repo"}}`
		var p webhookPayload
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.Issue.PullRequest == nil {
			t.Fatal("PR comment should have non-nil PullRequest")
		}
		if p.Issue.PullRequest.URL != "https://api.github.com/repos/owner/repo/pulls/5" {
			t.Errorf("unexpected PullRequest URL: %s", p.Issue.PullRequest.URL)
		}
	})
}

func TestTruncateLog(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		contains string
		notLong  bool // result should be shorter than input
	}{
		{
			name:     "short stays intact",
			input:    "one line",
			max:      5,
			contains: "one line",
		},
		{
			name:     "exactly max lines",
			input:    "line1\nline2\nline3",
			max:      3,
			contains: "line1",
		},
		{
			name:     "truncates to tail",
			input:    "line1\nline2\nline3\nline4\nline5\nline6\nline7",
			max:      2,
			contains: "line6",
			notLong:  true,
		},
		{
			name:     "truncated shows line count",
			input:    "a\nb\nc\nd\ne\nf",
			max:      2,
			contains: "(6 lines)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateLog(tt.input, tt.max)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("expected result to contain %q, got %q", tt.contains, result)
			}
			if tt.notLong && len(result) >= len(tt.input) {
				t.Errorf("expected truncation, but result (%d) >= input (%d)", len(result), len(tt.input))
			}
		})
	}
}
