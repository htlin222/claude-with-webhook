package main

import (
	"strings"
	"testing"
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
