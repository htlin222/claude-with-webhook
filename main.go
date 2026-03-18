package main

import (
	"bufio"
	"context"
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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
		ID   int    `json:"id"`
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

const (
	claudeTimeout = 5 * time.Minute
	gitTimeout    = 30 * time.Second
	maxErrorLen   = 500
)

var (
	issueMu   sync.Map // per-issue mutex keyed by "repo#number"
	semaphore chan struct{}

	// Patterns for files that should never be staged.
	dangerousFilePatterns = []string{
		".env*", "*.pem", "*.key", "*credential*", "*secret*", "*token*",
		"node_modules/", ".git/",
	}

	// Patterns for lines to redact from error output.
	secretLinePattern = regexp.MustCompile(`(?i)(token|key|secret|password|credential)`)
	absPathPattern    = regexp.MustCompile(`/Users/[^\s]+`)
)

func main() {
	cfg := loadConfig()

	maxConcurrent := 3
	if v := os.Getenv("MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxConcurrent = n
		}
	}
	semaphore = make(chan struct{}, maxConcurrent)
	log.Printf("max concurrent jobs: %d", maxConcurrent)

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
		// Concurrency limiter — drop if full.
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
		default:
			log.Printf("concurrency limit reached, skipping %s#%d", repo, payload.Issue.Number)
			return
		}

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
	reactToIssue(repo, repoDir, num)

	prompt := fmt.Sprintf("Plan how to implement the following GitHub issue.\n\nTitle: %s\n\nBody:\n%s", p.Issue.Title, p.Issue.Body)
	log.Printf("[%s#%d] claude started: planning", repo, num)
	plan, err := runCmd(repoDir, claudeTimeout, "claude", "-p", "--dangerously-skip-permissions", prompt)
	if err != nil {
		commentError(repo, repoDir, num, "Failed to generate plan", err)
		return
	}

	body := fmt.Sprintf("## Claude's Plan\n\n> Running with elevated permissions in isolated worktree\n\n%s\n\n---\n\n**Approve** to start implementation, or add extra instructions:\n\n```\nApprove\nApprove focus on error handling and add tests\nApprove 請用繁體中文寫註解\n```\n\nUse **@claude** to ask follow-up questions.", plan)
	if err := postIssueComment(repo, repoDir, num, body); err != nil {
		log.Printf("error commenting on #%d: %v", num, err)
	}
}

func handleIssueComment(cfg Config, repo, repoDir string, num int, p webhookPayload) {
	log.Printf("[%s#%d] comment from %s (type: %s): %s", repo, num, p.Comment.User.Login, p.Sender.Type, truncateLog(p.Comment.Body, 5))

	if p.Sender.Type == "Bot" {
		log.Printf("[%s#%d] skipping bot comment", repo, num)
		return
	}

	sender := p.Comment.User.Login
	if !cfg.AllowedUsers[sender] {
		log.Printf("[%s#%d] skipping non-allowed user %s", repo, num, sender)
		return
	}

	// Acknowledge the comment with 👀.
	reactToComment(repo, repoDir, p.Comment.ID)

	body := strings.TrimSpace(p.Comment.Body)
	firstLine := strings.ToLower(strings.SplitN(body, "\n", 2)[0])
	firstLine = strings.TrimSpace(firstLine)

	switch {
	case firstLine == "approve" || firstLine == "approved" || firstLine == "lgtm":
		// Anything after the first line is extra guidance.
		extra := ""
		if idx := strings.Index(body, "\n"); idx != -1 {
			extra = strings.TrimSpace(body[idx+1:])
		}
		if extra != "" {
			log.Printf("[%s#%d] approve with extra guidance: %s", repo, num, truncateLog(extra, 3))
		}
		handleApprove(cfg, repo, repoDir, num, p, extra)
	case strings.HasPrefix(firstLine, "approve ") || strings.HasPrefix(firstLine, "approved "):
		// "Approve focus on error handling" → single-line guidance
		extra := strings.TrimSpace(body[strings.Index(firstLine, " ")+1:])
		log.Printf("[%s#%d] approve with extra guidance: %s", repo, num, truncateLog(extra, 3))
		handleApprove(cfg, repo, repoDir, num, p, extra)
	case strings.HasPrefix(firstLine, "@claude"):
		handleFollowUp(cfg, repo, repoDir, num, p)
	default:
		log.Printf("[%s#%d] unmatched comment: %s", repo, num, truncateLog(body, 2))
	}
}

// truncateLog returns the last N lines of s for compact logging.
func truncateLog(s string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, " | ")
	}
	tail := lines[len(lines)-maxLines:]
	return fmt.Sprintf("...(%d lines) | %s", len(lines), strings.Join(tail, " | "))
}

func handleFollowUp(cfg Config, repo, repoDir string, num int, p webhookPayload) {
	log.Printf("[%s] follow-up on issue #%d", repo, num)

	discussion, err := runCmd(repoDir, gitTimeout, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--comments")
	if err != nil {
		commentError(repo, repoDir, num, "Failed to read issue discussion", err)
		return
	}

	prompt := fmt.Sprintf("You are helping with a GitHub issue. Read the full discussion below, including the original issue and all comments. The latest comment is a follow-up question or request directed at you. Respond helpfully.\n\n%s", discussion)
	log.Printf("[%s#%d] claude started: follow-up", repo, num)
	reply, err := runCmd(repoDir, claudeTimeout, "claude", "-p", "--dangerously-skip-permissions", prompt)
	if err != nil {
		commentError(repo, repoDir, num, "Claude follow-up failed", err)
		return
	}

	if err := postIssueComment(repo, repoDir, num, reply); err != nil {
		log.Printf("error commenting on #%d: %v", num, err)
	}
}

func handleApprove(cfg Config, repo, repoDir string, num int, p webhookPayload, extraGuidance string) {
	log.Printf("[%s] implementing issue #%d", repo, num)

	branch := fmt.Sprintf("issue-%d", num)
	worktreeDir := filepath.Join(repoDir, "worktrees", branch)

	// Skip if branch already exists (already processed).
	if _, err := runCmd(repoDir, gitTimeout, "git", "rev-parse", "--verify", branch); err == nil {
		log.Printf("branch %s already exists, skipping duplicate approve", branch)
		return
	}

	if _, err := runCmd(repoDir, gitTimeout, "git", "fetch", "origin", "main"); err != nil {
		commentError(repo, repoDir, num, "Failed to fetch origin/main", err)
		return
	}

	if _, err := runCmd(repoDir, gitTimeout, "git", "worktree", "add", worktreeDir, "-b", branch, "origin/main"); err != nil {
		commentError(repo, repoDir, num, "Failed to create worktree", err)
		return
	}

	success := false
	defer func() {
		if !success {
			cleanupWorktree(repoDir, worktreeDir, branch)
		}
	}()

	discussion, err := runCmd(repoDir, gitTimeout, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--comments")
	if err != nil {
		commentError(repo, repoDir, num, "Failed to read issue discussion", err)
		return
	}

	prompt := fmt.Sprintf("Implement the following GitHub issue. Read the full discussion below carefully, including all comments and follow-up questions, then make all necessary code changes.\n\n%s", discussion)
	if extraGuidance != "" {
		prompt += fmt.Sprintf("\n\n## Additional Guidance from Approver\n\nPay special attention to the following instruction — it takes priority over general discussion:\n\n%s", extraGuidance)
	}
	log.Printf("[%s#%d] claude started: implementing", repo, num)
	if _, err := runCmd(worktreeDir, claudeTimeout, "claude", "-p", "--dangerously-skip-permissions", prompt); err != nil {
		commentError(repo, repoDir, num, "Claude implementation failed", err)
		return
	}

	status, err := runCmd(worktreeDir, gitTimeout, "git", "status", "--porcelain")
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

	// Filtered git add — skip dangerous files.
	filesToAdd := filterSafeFiles(status)
	if len(filesToAdd) == 0 {
		postIssueComment(repo, repoDir, num, "All changed files were filtered out by security policy. Nothing to commit.")
		return
	}
	addArgs := append([]string{"add", "--"}, filesToAdd...)
	if _, err := runCmd(worktreeDir, gitTimeout, "git", addArgs...); err != nil {
		commentError(repo, repoDir, num, "Failed to stage changes", err)
		return
	}
	if _, err := runCmd(worktreeDir, gitTimeout, "git", "commit", "-m", commitMsg); err != nil {
		commentError(repo, repoDir, num, "Failed to commit", err)
		return
	}
	if _, err := runCmd(worktreeDir, gitTimeout, "git", "push", "-u", "origin", branch); err != nil {
		commentError(repo, repoDir, num, "Failed to push branch", err)
		return
	}

	prTitle := fmt.Sprintf("Fix #%d: %s", num, title)
	prBody := fmt.Sprintf("Closes #%d\n\nImplemented automatically by Claude.", num)
	prURL, err := runCmd(worktreeDir, gitTimeout, "gh", "pr", "create", "--title", prTitle, "--body", prBody, "--repo", repo)
	if err != nil {
		commentError(repo, repoDir, num, "Failed to create PR", err)
		return
	}

	prURL = strings.TrimSpace(prURL)
	postIssueComment(repo, repoDir, num, fmt.Sprintf("PR created: %s", prURL))
	success = true

	log.Printf("[%s] PR created for issue #%d: %s", repo, num, prURL)
}

// runCmd executes a command in the given directory with a timeout and returns combined output.
func runCmd(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	label := name
	if len(args) > 0 {
		label += " " + args[0]
	}

	if ctx.Err() == context.DeadlineExceeded {
		log.Printf("TIMEOUT: %s after %s", label, timeout)
		return string(out), fmt.Errorf("%s %s: timed out after %s", name, strings.Join(args, " "), timeout)
	}
	if err != nil {
		log.Printf("FAIL: %s (%s)", label, elapsed.Round(time.Millisecond))
		return string(out), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	if elapsed > 5*time.Second {
		log.Printf("  %s done (%s)", label, elapsed.Round(time.Millisecond))
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
	_, err := runCmd(repoDir, gitTimeout, "gh", "issue", "comment", strconv.Itoa(num), "--repo", repo, "--body", body)
	return err
}

// reactToIssue adds an 👀 emoji reaction to an issue.
func reactToIssue(repo, repoDir string, num int) {
	endpoint := fmt.Sprintf("repos/%s/issues/%d/reactions", repo, num)
	_, err := runCmd(repoDir, gitTimeout, "gh", "api", endpoint, "-f", "content=eyes")
	if err != nil {
		log.Printf("failed to react to issue #%d: %v", num, err)
	}
}

// reactToComment adds an 👀 emoji reaction to a comment.
func reactToComment(repo, repoDir string, commentID int) {
	endpoint := fmt.Sprintf("repos/%s/issues/comments/%d/reactions", repo, commentID)
	_, err := runCmd(repoDir, gitTimeout, "gh", "api", endpoint, "-f", "content=eyes")
	if err != nil {
		log.Printf("failed to react to comment %d: %v", commentID, err)
	}
}

// commentError posts a sanitized error message on the issue.
func commentError(repo, repoDir string, num int, msg string, err error) {
	log.Printf("error on %s#%d: %s: %v", repo, num, msg, err)
	sanitized := sanitizeError(err.Error())
	body := fmt.Sprintf("**Error**: %s\n\n```\n%s\n```", msg, sanitized)
	postIssueComment(repo, repoDir, num, body)
}

// sanitizeError truncates, strips secrets, and redacts paths from error output.
func sanitizeError(s string) string {
	// Strip lines containing secret-like keywords.
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if !secretLinePattern.MatchString(line) {
			lines = append(lines, line)
		}
	}
	s = strings.Join(lines, "\n")

	// Redact absolute file paths.
	s = absPathPattern.ReplaceAllString(s, "<redacted-path>/")

	// Truncate.
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen] + "\n... (truncated)"
	}
	return s
}

// cleanupWorktree removes a worktree and its branch.
func cleanupWorktree(repoDir, dir, branch string) {
	log.Printf("cleaning up worktree %s", dir)
	runCmd(repoDir, gitTimeout, "git", "worktree", "remove", "--force", dir)
	runCmd(repoDir, gitTimeout, "git", "branch", "-D", branch)
}

// filterSafeFiles parses `git status --porcelain` output and returns files safe to stage.
func filterSafeFiles(porcelain string) []string {
	var safe []string
	for _, line := range strings.Split(porcelain, "\n") {
		// Porcelain format: "XY filename" — XY is exactly 2 chars, then a space.
		// Lines are NOT trimmed because leading spaces are meaningful status chars.
		if len(line) < 4 {
			continue
		}
		file := line[3:]
		if idx := strings.Index(file, " -> "); idx != -1 {
			file = file[idx+4:]
		}
		file = strings.TrimSpace(file)

		if file == "" {
			continue
		}
		if isDangerousFile(file) {
			log.Printf("WARNING: skipping dangerous file: %s", file)
			continue
		}
		safe = append(safe, file)
	}
	return safe
}

// isDangerousFile checks if a file matches any dangerous pattern.
func isDangerousFile(file string) bool {
	base := filepath.Base(file)
	for _, pattern := range dangerousFilePatterns {
		// Directory prefix match.
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(file, pattern) {
			return true
		}
		// Glob match against base name.
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
		// Glob match against full path.
		if matched, _ := filepath.Match(pattern, file); matched {
			return true
		}
	}
	return false
}
