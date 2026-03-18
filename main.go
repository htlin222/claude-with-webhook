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
	Repos         map[string]string // "owner/repo" → local path
	BaseDir       string            // directory where server lives (~/.claude-webhook)
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

var issueMu sync.Map // per-issue mutex keyed by "repo#number"

func main() {
	cfg := loadConfig()

	mux := http.NewServeMux()

	// Global health check.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Catch-all handler for /{owner}/{repo}/webhook routes.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")

		// Expect: {owner}/{repo}/webhook or {owner}/{repo}/health
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}

		repoFullName := parts[0] + "/" + parts[1]
		action := parts[2]

		switch action {
		case "webhook":
			handleWebhook(w, r, cfg, repoFullName)
		case "health":
			repoDir, ok := cfg.Repos[repoFullName]
			if !ok {
				http.Error(w, fmt.Sprintf("repo %s not registered", repoFullName), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"status": "ok",
				"repo":   repoFullName,
				"path":   repoDir,
			})
		default:
			http.NotFound(w, r)
		}
	})

	log.Printf("registered repos:")
	for repo, dir := range cfg.Repos {
		log.Printf("  %s → %s", repo, dir)
	}

	addr := ":" + cfg.Port
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// loadConfig reads configuration from environment variables, loading .env first.
func loadConfig() Config {
	// Resolve base directory (where the server binary lives).
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("failed to resolve executable path: %v", err)
	}
	baseDir := filepath.Dir(exe)

	loadDotenv(filepath.Join(baseDir, ".env"))

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

	repos := loadRepos(filepath.Join(baseDir, "repos.conf"))

	return Config{
		WebhookSecret: secret,
		AllowedUsers:  allowed,
		Port:          port,
		Repos:         repos,
		BaseDir:       baseDir,
	}
}

// loadRepos reads repos.conf: each line is "owner/repo=/local/path".
func loadRepos(path string) map[string]string {
	repos := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		log.Printf("no repos.conf found at %s", path)
		return repos
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		repo, dir, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		repo = strings.TrimSpace(repo)
		dir = strings.TrimSpace(dir)
		if repo != "" && dir != "" {
			repos[repo] = dir
		}
	}
	return repos
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
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

func handleWebhook(w http.ResponseWriter, r *http.Request, cfg Config, repoFromURL string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
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

	// Verify the webhook payload matches the URL route.
	repo := payload.Repository.FullName
	if repo != repoFromURL {
		log.Printf("repo mismatch: URL=%s payload=%s", repoFromURL, repo)
		http.Error(w, "repo mismatch", http.StatusBadRequest)
		return
	}

	// Look up local path for this repo.
	repoDir, ok := cfg.Repos[repo]
	if !ok {
		log.Printf("repo %s not registered in repos.conf", repo)
		http.Error(w, fmt.Sprintf("repo %s not registered", repo), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)

	go func() {
		num := payload.Issue.Number
		lockKey := fmt.Sprintf("%s#%d", repo, num)

		mu, _ := issueMu.LoadOrStore(lockKey, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()

		switch event {
		case "issues":
			if payload.Action == "opened" {
				handleIssueOpened(cfg, repo, repoDir, num, payload)
			}
		case "issue_comment":
			if payload.Action == "created" {
				handleIssueComment(cfg, repo, repoDir, num, payload)
			}
		}
	}()
}

func handleIssueOpened(cfg Config, repo, repoDir string, num int, p webhookPayload) {
	sender := p.Issue.User.Login
	if !cfg.AllowedUsers[sender] {
		log.Printf("ignoring issue #%d from non-allowed user %s", num, sender)
		return
	}

	log.Printf("[%s] planning for issue #%d: %s", repo, num, p.Issue.Title)

	prompt := fmt.Sprintf("Plan how to implement the following GitHub issue.\n\nTitle: %s\n\nBody:\n%s", p.Issue.Title, p.Issue.Body)
	plan, err := runCmd(repoDir, "claude", "-p", "--dangerously-skip-permissions", prompt)
	if err != nil {
		commentError(repo, repoDir, num, "Failed to generate plan", err)
		return
	}

	body := fmt.Sprintf("## Claude's Plan\n\n%s\n\n---\n\nReply **Approve** to start implementation.\nUse **@claude** to ask follow-up questions.", plan)
	if err := postIssueComment(repo, repoDir, num, body); err != nil {
		log.Printf("error commenting on #%d: %v", num, err)
	}
}

func handleIssueComment(cfg Config, repo, repoDir string, num int, p webhookPayload) {
	if p.Sender.Type == "Bot" {
		return
	}

	sender := p.Comment.User.Login
	if !cfg.AllowedUsers[sender] {
		return
	}

	body := strings.TrimSpace(strings.ToLower(p.Comment.Body))

	switch {
	case body == "approve" || body == "approved" || body == "lgtm":
		handleApprove(cfg, repo, repoDir, num, p)
	case strings.HasPrefix(body, "@claude"):
		handleFollowUp(cfg, repo, repoDir, num, p)
	}
}

func handleFollowUp(cfg Config, repo, repoDir string, num int, p webhookPayload) {
	log.Printf("[%s] follow-up on issue #%d", repo, num)

	discussion, err := runCmd(repoDir, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--comments")
	if err != nil {
		commentError(repo, repoDir, num, "Failed to read issue discussion", err)
		return
	}

	prompt := fmt.Sprintf("You are helping with a GitHub issue. Read the full discussion below, including the original issue and all comments. The latest comment is a follow-up question or request directed at you. Respond helpfully.\n\n%s", discussion)
	reply, err := runCmd(repoDir, "claude", "-p", "--dangerously-skip-permissions", prompt)
	if err != nil {
		commentError(repo, repoDir, num, "Claude follow-up failed", err)
		return
	}

	if err := postIssueComment(repo, repoDir, num, reply); err != nil {
		log.Printf("error commenting on #%d: %v", num, err)
	}
}

func handleApprove(cfg Config, repo, repoDir string, num int, p webhookPayload) {
	log.Printf("[%s] implementing issue #%d", repo, num)

	branch := fmt.Sprintf("issue-%d", num)
	worktreeDir := filepath.Join(repoDir, "worktrees", branch)

	// Skip if branch already exists (already processed).
	if _, err := runCmd(repoDir, "git", "rev-parse", "--verify", branch); err == nil {
		log.Printf("branch %s already exists, skipping duplicate approve", branch)
		return
	}

	if _, err := runCmd(repoDir, "git", "fetch", "origin", "main"); err != nil {
		commentError(repo, repoDir, num, "Failed to fetch origin/main", err)
		return
	}

	if _, err := runCmd(repoDir, "git", "worktree", "add", worktreeDir, "-b", branch, "origin/main"); err != nil {
		commentError(repo, repoDir, num, "Failed to create worktree", err)
		return
	}

	success := false
	defer func() {
		if !success {
			cleanupWorktree(repoDir, worktreeDir, branch)
		}
	}()

	discussion, err := runCmd(repoDir, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--comments")
	if err != nil {
		commentError(repo, repoDir, num, "Failed to read issue discussion", err)
		return
	}

	prompt := fmt.Sprintf("Implement the following GitHub issue. Read the full discussion below carefully, including all comments and follow-up questions, then make all necessary code changes.\n\n%s", discussion)
	if _, err := runCmd(worktreeDir, "claude", "-p", "--dangerously-skip-permissions", prompt); err != nil {
		commentError(repo, repoDir, num, "Claude implementation failed", err)
		return
	}

	status, err := runCmd(worktreeDir, "git", "status", "--porcelain")
	if err != nil {
		commentError(repo, repoDir, num, "Failed to check git status", err)
		return
	}
	if strings.TrimSpace(status) == "" {
		postIssueComment(repo, repoDir, num, "No changes were made by Claude. Nothing to commit.")
		return
	}

	title := p.Issue.Title
	commitMsg := fmt.Sprintf("Implement #%d: %s", num, title)

	if _, err := runCmd(worktreeDir, "git", "add", "-A"); err != nil {
		commentError(repo, repoDir, num, "Failed to stage changes", err)
		return
	}
	if _, err := runCmd(worktreeDir, "git", "commit", "-m", commitMsg); err != nil {
		commentError(repo, repoDir, num, "Failed to commit", err)
		return
	}
	if _, err := runCmd(worktreeDir, "git", "push", "-u", "origin", branch); err != nil {
		commentError(repo, repoDir, num, "Failed to push branch", err)
		return
	}

	prTitle := fmt.Sprintf("Fix #%d: %s", num, title)
	prBody := fmt.Sprintf("Closes #%d\n\nImplemented automatically by Claude.", num)
	prURL, err := runCmd(worktreeDir, "gh", "pr", "create", "--title", prTitle, "--body", prBody, "--repo", repo)
	if err != nil {
		commentError(repo, repoDir, num, "Failed to create PR", err)
		return
	}

	prURL = strings.TrimSpace(prURL)
	postIssueComment(repo, repoDir, num, fmt.Sprintf("PR created: %s", prURL))
	success = true

	log.Printf("[%s] PR created for issue #%d: %s", repo, num, prURL)
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
func postIssueComment(repo, repoDir string, num int, body string) error {
	_, err := runCmd(repoDir, "gh", "issue", "comment", strconv.Itoa(num), "--repo", repo, "--body", body)
	return err
}

// commentError posts an error message on the issue.
func commentError(repo, repoDir string, num int, msg string, err error) {
	log.Printf("error on %s#%d: %s: %v", repo, num, msg, err)
	body := fmt.Sprintf("**Error**: %s\n\n```\n%v\n```", msg, err)
	postIssueComment(repo, repoDir, num, body)
}

// cleanupWorktree removes a worktree and its branch.
func cleanupWorktree(repoDir, dir, branch string) {
	log.Printf("cleaning up worktree %s", dir)
	runCmd(repoDir, "git", "worktree", "remove", "--force", dir)
	runCmd(repoDir, "git", "branch", "-D", branch)
}
