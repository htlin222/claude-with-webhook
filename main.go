package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	WebhookSecret string
	AllowedUsers  map[string]bool
	Port          string
}

// Minimal JSON structures for GitHub webhook payloads.
type webhookPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Sender struct {
		Login string `json:"login"`
		Type  string `json:"type"` // "User" or "Bot"
	} `json:"sender"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

var (
	issueMu   sync.Map // per-issue mutex keyed by "repo#number"
	repoRoot  string
)

func main() {
	cfg := loadConfig()

	// Resolve repo root: if REPO_ROOT is set use that, otherwise use parent of
	// the executable's directory (supports webhookd/ layout).
	root := os.Getenv("REPO_ROOT")
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			log.Fatalf("failed to resolve executable path: %v", err)
		}
		root = filepath.Dir(filepath.Dir(exe)) // webhookd/../ = repo root
	}
	var err error
	repoRoot, err = filepath.Abs(root)
	if err != nil {
		log.Fatalf("failed to resolve repo root: %v", err)
	}
	log.Printf("repo root: %s", repoRoot)

	mux := http.NewServeMux()
	mux.HandleFunc("/claude-with-webhook/webhook", func(w http.ResponseWriter, r *http.Request) {
		handleWebhook(w, r, cfg)
	})
	mux.HandleFunc("/claude-with-webhook/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	addr := ":" + cfg.Port
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// loadConfig reads configuration from environment variables, loading .env first.
func loadConfig() Config {
	loadDotenv(".env")

	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("GITHUB_WEBHOOK_SECRET is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	allowed := make(map[string]bool)
	for _, u := range strings.Split(os.Getenv("ALLOWED_USERS"), ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			allowed[u] = true
		}
	}

	return Config{
		WebhookSecret: secret,
		AllowedUsers:  allowed,
		Port:          port,
	}
}

// loadDotenv reads a .env file and sets any variables not already in the environment.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // missing .env is fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Don't override existing env vars.
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

func handleWebhook(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if !verifySignature(body, r.Header.Get("X-Hub-Signature-256"), cfg.WebhookSecret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "issues" && event != "issue_comment" {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Return 200 immediately; process asynchronously.
	w.WriteHeader(http.StatusOK)

	go func() {
		repo := payload.Repository.FullName
		num := payload.Issue.Number
		lockKey := fmt.Sprintf("%s#%d", repo, num)

		// Per-issue mutex to prevent duplicate processing.
		mu, _ := issueMu.LoadOrStore(lockKey, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()

		switch event {
		case "issues":
			if payload.Action == "opened" {
				handleIssueOpened(cfg, repo, num, payload)
			}
		case "issue_comment":
			if payload.Action == "created" {
				handleIssueComment(cfg, repo, num, payload)
			}
		}
	}()
}

func handleIssueOpened(cfg Config, repo string, num int, p webhookPayload) {
	sender := p.Issue.User.Login
	if !cfg.AllowedUsers[sender] {
		log.Printf("ignoring issue #%d from non-allowed user %s", num, sender)
		return
	}

	log.Printf("planning for issue #%d: %s", num, p.Issue.Title)

	prompt := fmt.Sprintf("Plan how to implement the following GitHub issue.\n\nTitle: %s\n\nBody:\n%s", p.Issue.Title, p.Issue.Body)
	plan, err := runCmd(repoRoot, "claude", "-p", "--dangerously-skip-permissions", prompt)
	if err != nil {
		commentError(repo, num, "Failed to generate plan", err)
		return
	}

	body := fmt.Sprintf("## Claude's Plan\n\n%s\n\n---\n\nReply **Approve** to start implementation.", plan)
	if err := postIssueComment(repo, num, body); err != nil {
		log.Printf("error commenting on #%d: %v", num, err)
	}
}

func handleIssueComment(cfg Config, repo string, num int, p webhookPayload) {
	// Ignore bot comments (e.g. our own plan comments contain "Approve" text).
	if p.Sender.Type == "Bot" {
		return
	}

	sender := p.Comment.User.Login
	if !cfg.AllowedUsers[sender] {
		return
	}

	body := strings.TrimSpace(strings.ToLower(p.Comment.Body))

	// Exact "approve" → implement. "@claude ..." → follow-up. Otherwise ignore.
	switch {
	case body == "approve" || body == "approved" || body == "lgtm":
		handleApprove(cfg, repo, num, p)
	case strings.HasPrefix(body, "@claude"):
		handleFollowUp(cfg, repo, num, p)
	}
}

func handleFollowUp(cfg Config, repo string, num int, p webhookPayload) {
	log.Printf("follow-up on issue #%d", num)

	// Get full issue discussion.
	discussion, err := runCmd(repoRoot, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--comments")
	if err != nil {
		commentError(repo, num, "Failed to read issue discussion", err)
		return
	}

	prompt := fmt.Sprintf("You are helping with a GitHub issue. Read the full discussion below, including the original issue and all comments. The latest comment is a follow-up question or request directed at you. Respond helpfully.\n\n%s", discussion)
	reply, err := runCmd(repoRoot, "claude", "-p", "--dangerously-skip-permissions", prompt)
	if err != nil {
		commentError(repo, num, "Claude follow-up failed", err)
		return
	}

	if err := postIssueComment(repo, num, reply); err != nil {
		log.Printf("error commenting on #%d: %v", num, err)
	}
}

func handleApprove(cfg Config, repo string, num int, p webhookPayload) {
	log.Printf("implementing issue #%d", num)

	branch := fmt.Sprintf("issue-%d", num)
	worktreeDir := filepath.Join(repoRoot, "worktrees", branch)

	// Skip if branch already exists (already processed).
	if _, err := runCmd(repoRoot, "git", "rev-parse", "--verify", branch); err == nil {
		log.Printf("branch %s already exists, skipping duplicate approve", branch)
		return
	}

	// Fetch latest main.
	if _, err := runCmd(repoRoot, "git", "fetch", "origin", "main"); err != nil {
		commentError(repo, num, "Failed to fetch origin/main", err)
		return
	}

	// Create worktree.
	if _, err := runCmd(repoRoot, "git", "worktree", "add", worktreeDir, "-b", branch, "origin/main"); err != nil {
		commentError(repo, num, "Failed to create worktree", err)
		return
	}

	// Cleanup on failure.
	success := false
	defer func() {
		if !success {
			cleanupWorktree(worktreeDir, branch)
		}
	}()

	// Get full issue discussion.
	discussion, err := runCmd(repoRoot, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--comments")
	if err != nil {
		commentError(repo, num, "Failed to read issue discussion", err)
		return
	}

	// Run Claude to implement — include full discussion so follow-up
	// questions and context from all comments are visible.
	prompt := fmt.Sprintf("Implement the following GitHub issue. Read the full discussion below carefully, including all comments and follow-up questions, then make all necessary code changes.\n\n%s", discussion)
	if _, err := runCmd(worktreeDir, "claude", "-p", "--dangerously-skip-permissions", prompt); err != nil {
		commentError(repo, num, "Claude implementation failed", err)
		return
	}

	// Check for changes.
	status, err := runCmd(worktreeDir, "git", "status", "--porcelain")
	if err != nil {
		commentError(repo, num, "Failed to check git status", err)
		return
	}
	if strings.TrimSpace(status) == "" {
		postIssueComment(repo, num, "No changes were made by Claude. Nothing to commit.")
		return
	}

	// Commit and push.
	title := p.Issue.Title
	commitMsg := fmt.Sprintf("Implement #%d: %s", num, title)

	if _, err := runCmd(worktreeDir, "git", "add", "-A"); err != nil {
		commentError(repo, num, "Failed to stage changes", err)
		return
	}
	if _, err := runCmd(worktreeDir, "git", "commit", "-m", commitMsg); err != nil {
		commentError(repo, num, "Failed to commit", err)
		return
	}
	if _, err := runCmd(worktreeDir, "git", "push", "-u", "origin", branch); err != nil {
		commentError(repo, num, "Failed to push branch", err)
		return
	}

	// Create PR.
	prTitle := fmt.Sprintf("Fix #%d: %s", num, title)
	prBody := fmt.Sprintf("Closes #%d\n\nImplemented automatically by Claude.", num)
	prURL, err := runCmd(worktreeDir, "gh", "pr", "create", "--title", prTitle, "--body", prBody, "--repo", repo)
	if err != nil {
		commentError(repo, num, "Failed to create PR", err)
		return
	}

	prURL = strings.TrimSpace(prURL)
	postIssueComment(repo, num, fmt.Sprintf("PR created: %s", prURL))
	success = true

	log.Printf("PR created for issue #%d: %s", num, prURL)
}

// runCmd executes a command in the given directory and returns combined output.
func runCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	log.Printf("exec: %s %s (dir: %s)", name, strings.Join(args, " "), dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// verifySignature checks the HMAC-SHA256 signature from GitHub.
func verifySignature(payload []byte, header, secret string) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hmac.Equal(sig, mac.Sum(nil))
}

// postIssueComment posts a comment on a GitHub issue using gh CLI.
func postIssueComment(repo string, num int, body string) error {
	_, err := runCmd(repoRoot, "gh", "issue", "comment", strconv.Itoa(num), "--repo", repo, "--body", body)
	return err
}

// commentError posts an error message on the issue.
func commentError(repo string, num int, msg string, err error) {
	log.Printf("error on #%d: %s: %v", num, msg, err)
	body := fmt.Sprintf("**Error**: %s\n\n```\n%v\n```", msg, err)
	postIssueComment(repo, num, body)
}

// cleanupWorktree removes a worktree and its branch.
func cleanupWorktree(dir, branch string) {
	log.Printf("cleaning up worktree %s", dir)
	runCmd(repoRoot, "git", "worktree", "remove", "--force", dir)
	runCmd(repoRoot, "git", "branch", "-D", branch)
}
