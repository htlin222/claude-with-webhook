package main

import (
	"bufio"
	"bytes"
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
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	WebhookSecret string
	AllowedUsers  map[string]bool
	BotUsername    string // GitHub username the bot posts as; its own comments are ignored
	Port          string
	BaseDir       string // directory where server lives (~/.claude-webhook)

	reposMu sync.RWMutex
	repos   map[string]string // "owner/repo" → local path
}

// GetRepo returns the local path for a repo, safe for concurrent access.
func (c *Config) GetRepo(name string) (string, bool) {
	c.reposMu.RLock()
	defer c.reposMu.RUnlock()
	dir, ok := c.repos[name]
	return dir, ok
}

// ReloadRepos re-reads repos.conf from disk.
func (c *Config) ReloadRepos() {
	repos := loadRepos(filepath.Join(c.BaseDir, "repos.conf"))
	c.reposMu.Lock()
	c.repos = repos
	c.reposMu.Unlock()
	log.Printf("reloaded repos.conf: %d repo(s)", len(repos))
	for repo, dir := range repos {
		log.Printf("  %s → %s", repo, dir)
	}
}

// AllRepos returns a snapshot of the current repo map.
func (c *Config) AllRepos() map[string]string {
	c.reposMu.RLock()
	defer c.reposMu.RUnlock()
	snapshot := make(map[string]string, len(c.repos))
	for k, v := range c.repos {
		snapshot[k] = v
	}
	return snapshot
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

// Build-time variables, set via -ldflags.
var (
	version   = "dev"
	buildTime = "unknown"
)

// Stream event types for claude --output-format stream-json
type streamEvent struct {
	Type         string         `json:"type"`
	Subtype      string         `json:"subtype,omitempty"`
	Message      *streamMessage `json:"message,omitempty"`
	Result       string         `json:"result,omitempty"`
	TotalCostUSD float64        `json:"total_cost_usd,omitempty"`
	DurationMS   int64          `json:"duration_ms,omitempty"`
	NumTurns     int            `json:"num_turns,omitempty"`
	IsError      bool           `json:"is_error,omitempty"`
}

type streamMessage struct {
	Content []streamContent `json:"content"`
}

type streamContent struct {
	Type string `json:"type"` // "text", "tool_use", "thinking", "tool_result"
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"` // tool name for tool_use events
}

type streamResult struct {
	Text         string
	TotalCostUSD float64
	DurationMS   int64
	NumTurns     int
}

const (
	planTimeout      = 30 * time.Minute
	followUpTimeout  = 30 * time.Minute
	implementTimeout = 60 * time.Minute
	gitTimeout       = 30 * time.Second
	maxErrorLen      = 500

	spinnerImg = `<div align="center">

![](https://raw.githubusercontent.com/htlin222/claude-with-webhook/e19f046c9ae189880d65d778f2cb1305978cc52c/assests/spinner.svg)

</div>`
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
	log.Printf("claude-webhook-server %s (built %s)", version, buildTime)

	maxConcurrent := 3
	if v := os.Getenv("MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxConcurrent = n
		}
	}
	semaphore = make(chan struct{}, maxConcurrent)
	log.Printf("max concurrent jobs: %d", maxConcurrent)

	// Reload repos.conf on SIGHUP (sent by remote-install.sh after registration).
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			log.Printf("received SIGHUP, reloading repos.conf...")
			cfg.ReloadRepos()
		}
	}()

	mux := http.NewServeMux()

	// Global health check.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Version endpoint.
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version":    version,
			"build_time": buildTime,
		})
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
			repoDir, ok := cfg.GetRepo(repoFullName)
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
	for repo, dir := range cfg.AllRepos() {
		log.Printf("  %s → %s", repo, dir)
	}

	addr := ":" + cfg.Port
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// loadConfig reads configuration from environment variables, loading .env first.
func loadConfig() *Config {
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

	return &Config{
		WebhookSecret: secret,
		AllowedUsers:  allowed,
		BotUsername:    os.Getenv("BOT_USERNAME"),
		Port:          port,
		repos:         repos,
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

func handleWebhook(w http.ResponseWriter, r *http.Request, cfg *Config, repoFromURL string) {
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
	repoDir, ok := cfg.GetRepo(repo)
	if !ok {
		log.Printf("repo %s not registered in repos.conf", repo)
		http.Error(w, fmt.Sprintf("repo %s not registered", repo), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)

	go func() {
		num := payload.Issue.Number
		lockKey := fmt.Sprintf("%s#%d", repo, num)

		// Per-issue mutex: if this issue is already being processed,
		// block until it finishes rather than dropping the event.
		mu, _ := issueMu.LoadOrStore(lockKey, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()

		// Concurrency limiter — block-wait for a slot.
		semaphore <- struct{}{}
		defer func() { <-semaphore }()

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

func handleIssueOpened(cfg *Config, repo, repoDir string, num int, p webhookPayload) {
	sender := p.Issue.User.Login
	if !cfg.AllowedUsers[sender] {
		log.Printf("ignoring issue #%d from non-allowed user %s", num, sender)
		return
	}

	reactToIssue(repo, repoDir, num)
	runPlan(repo, repoDir, num, p.Issue.Title, p.Issue.Body)
}

// handlePlan re-triggers planning from a comment (e.g. when the initial webhook was missed).
func handlePlan(cfg *Config, repo, repoDir string, num int, p webhookPayload) {
	log.Printf("[%s] re-planning issue #%d via comment", repo, num)

	// Fetch issue title and body since the comment payload doesn't include them.
	title, err := runCmd(repoDir, gitTimeout, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--json", "title,body", "--jq", ".title")
	if err != nil {
		commentError(repo, repoDir, num, "Failed to fetch issue details", err)
		return
	}
	body, err := runCmd(repoDir, gitTimeout, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--json", "body", "--jq", ".body")
	if err != nil {
		commentError(repo, repoDir, num, "Failed to fetch issue details", err)
		return
	}

	runPlan(repo, repoDir, num, strings.TrimSpace(title), strings.TrimSpace(body))
}

// runPlan generates a Claude plan for an issue and posts it as a comment.
func runPlan(repo, repoDir string, num int, title, issueBody string) {
	log.Printf("[%s] planning for issue #%d: %s", repo, num, title)

	updateComment, _ := postProgressComment(repo, repoDir, num, fmt.Sprintf("🤖 Planning…\n\n%s", spinnerImg))

	prompt := fmt.Sprintf("Plan how to implement the following GitHub issue.\n\nTitle: %s\n\nBody:\n%s", title, issueBody)
	log.Printf("[%s#%d] claude started: planning", repo, num)
	result, err := runClaudeStreaming(repoDir, planTimeout, func(partial string) {
		updateComment(progressBody("Planning", partial))
	}, prompt)
	if err != nil {
		updateComment(formatError("Failed to generate plan", err))
		return
	}

	body := fmt.Sprintf("## Claude's Plan\n\n> Running with elevated permissions in isolated worktree\n\n%s\n\n---\n\nComment **@claude** to interact:\n\n```\n@claude approve\n@claude approve focus on error handling and add tests\n@claude approve 請用繁體中文寫註解\n@claude plan (re-generate this plan)\n@claude <follow-up question>\n```%s", result.Text, formatMetadataFooter(result))
	updateComment(body)
}

// classifyComment determines what action to take on a comment.
// Returns: "skip-bot", "skip-self", "skip-user", "skip-no-prefix",
//
//	"skip-bare-mention", "approve", "plan", "followup"
func classifyComment(cfg *Config, sender, senderType, body string) string {
	if senderType == "Bot" {
		return "skip-bot"
	}

	if cfg.BotUsername != "" && strings.EqualFold(sender, cfg.BotUsername) {
		return "skip-self"
	}

	if !cfg.AllowedUsers[sender] {
		return "skip-user"
	}

	trimmed := strings.TrimSpace(body)
	firstLine := strings.ToLower(strings.SplitN(trimmed, "\n", 2)[0])
	firstLine = strings.TrimSpace(firstLine)

	if !strings.HasPrefix(firstLine, "@claude") {
		return "skip-no-prefix"
	}

	cmd := strings.TrimSpace(strings.TrimPrefix(firstLine, "@claude"))

	switch {
	case cmd == "approve" || cmd == "approved" || cmd == "lgtm":
		return "approve"
	case strings.HasPrefix(cmd, "approve ") || strings.HasPrefix(cmd, "approved "):
		return "approve"
	case cmd == "plan":
		return "plan"
	case cmd == "":
		return "skip-bare-mention"
	default:
		return "followup"
	}
}

func handleIssueComment(cfg *Config, repo, repoDir string, num int, p webhookPayload) {
	log.Printf("[%s#%d] comment from %s (type: %s): %s", repo, num, p.Comment.User.Login, p.Sender.Type, truncateLog(p.Comment.Body, 5))

	action := classifyComment(cfg, p.Comment.User.Login, p.Sender.Type, p.Comment.Body)
	switch action {
	case "skip-bot":
		log.Printf("[%s#%d] skipping bot comment", repo, num)
		return
	case "skip-self":
		log.Printf("[%s#%d] skipping own comment", repo, num)
		return
	case "skip-user":
		log.Printf("[%s#%d] skipping non-allowed user %s", repo, num, p.Comment.User.Login)
		return
	case "skip-no-prefix":
		log.Printf("[%s#%d] ignoring comment without @claude prefix: %s", repo, num, truncateLog(p.Comment.Body, 2))
		return
	case "skip-bare-mention":
		log.Printf("[%s#%d] ignoring bare @claude mention", repo, num)
		return
	}

	// Acknowledge the comment with 👀.
	reactToComment(repo, repoDir, p.Comment.ID)

	body := strings.TrimSpace(p.Comment.Body)
	cmd := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.SplitN(body, "\n", 2)[0]), "@claude"))

	switch action {
	case "approve":
		extra := ""
		if cmd == "approve" || cmd == "approved" || cmd == "lgtm" {
			// Anything after the first line is extra guidance.
			if idx := strings.Index(body, "\n"); idx != -1 {
				extra = strings.TrimSpace(body[idx+1:])
			}
		} else {
			// "@claude approve focus on error handling" → single-line guidance
			extra = strings.TrimSpace(cmd[strings.Index(cmd, " ")+1:])
		}
		if extra != "" {
			log.Printf("[%s#%d] approve with extra guidance: %s", repo, num, truncateLog(extra, 3))
		}
		handleApprove(cfg, repo, repoDir, num, p, extra)
	case "plan":
		handlePlan(cfg, repo, repoDir, num, p)
	case "followup":
		handleFollowUp(cfg, repo, repoDir, num, p)
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

func handleFollowUp(cfg *Config, repo, repoDir string, num int, p webhookPayload) {
	log.Printf("[%s] follow-up on issue #%d", repo, num)

	updateComment, _ := postProgressComment(repo, repoDir, num, fmt.Sprintf("🤖 Thinking…\n\n%s", spinnerImg))

	discussion, err := runCmd(repoDir, gitTimeout, "gh", "issue", "view", strconv.Itoa(num), "--repo", repo, "--comments")
	if err != nil {
		updateComment(formatError("Failed to read issue discussion", err))
		return
	}

	prompt := fmt.Sprintf("You are helping with a GitHub issue. Read the full discussion below, including the original issue and all comments. The latest comment is a follow-up question or request directed at you. Respond helpfully.\n\n%s", discussion)
	log.Printf("[%s#%d] claude started: follow-up", repo, num)
	result, err := runClaudeStreaming(repoDir, followUpTimeout, func(partial string) {
		updateComment(progressBody("Thinking", partial))
	}, prompt)
	if err != nil {
		updateComment(formatError("Claude follow-up failed", err))
		return
	}

	updateComment(result.Text + formatMetadataFooter(result))
}

func handleApprove(cfg *Config, repo, repoDir string, num int, p webhookPayload, extraGuidance string) {
	log.Printf("[%s] implementing issue #%d", repo, num)

	branch := fmt.Sprintf("issue-%d", num)
	worktreeDir := filepath.Join(repoDir, "worktrees", branch)

	// Skip if branch already exists (already processed).
	if branchExists(repoDir, branch) {
		log.Printf("branch %s already exists, skipping duplicate approve", branch)
		return
	}

	updateComment, deleteSpinner := postProgressComment(repo, repoDir, num, fmt.Sprintf("🤖 Implementing…\n\n%s", spinnerImg))

	if _, err := runCmd(repoDir, gitTimeout, "git", "fetch", "origin", "main"); err != nil {
		updateComment(formatError("Failed to fetch origin/main", err))
		return
	}

	if _, err := runCmd(repoDir, gitTimeout, "git", "worktree", "add", worktreeDir, "-b", branch, "origin/main"); err != nil {
		updateComment(formatError("Failed to create worktree", err))
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
		updateComment(formatError("Failed to read issue discussion", err))
		return
	}

	prompt := fmt.Sprintf("Implement the following GitHub issue. Read the full discussion below carefully, including all comments and follow-up questions, then make all necessary code changes.\n\n%s", discussion)
	if extraGuidance != "" {
		prompt += fmt.Sprintf("\n\n## Additional Guidance from Approver\n\nPay special attention to the following instruction — it takes priority over general discussion:\n\n%s", extraGuidance)
	}
	log.Printf("[%s#%d] claude started: implementing", repo, num)
	result, err := runClaudeStreaming(worktreeDir, implementTimeout, func(partial string) {
		updateComment(progressBody("Implementing", partial))
	}, prompt)
	if err != nil {
		updateComment(formatError("Claude implementation failed", err))
		return
	}
	_ = result // implementation uses git status for results, not claude output

	status, err := runCmd(worktreeDir, gitTimeout, "git", "status", "--porcelain")
	if err != nil {
		updateComment(formatError("Failed to check git status", err))
		return
	}
	if strings.TrimSpace(status) == "" {
		updateComment("No changes were made by Claude. Nothing to commit.")
		return
	}

	title := p.Issue.Title
	commitMsg := fmt.Sprintf("Implement #%d: %s", num, title)

	// Filtered git add — skip dangerous files.
	filesToAdd := filterSafeFiles(status)
	if len(filesToAdd) == 0 {
		updateComment("All changed files were filtered out by security policy. Nothing to commit.")
		return
	}
	addArgs := append([]string{"add", "--"}, filesToAdd...)
	if _, err := runCmd(worktreeDir, gitTimeout, "git", addArgs...); err != nil {
		updateComment(formatError("Failed to stage changes", err))
		return
	}
	if _, err := runCmd(worktreeDir, gitTimeout, "git", "commit", "-m", commitMsg); err != nil {
		updateComment(formatError("Failed to commit", err))
		return
	}
	if _, err := runCmd(worktreeDir, gitTimeout, "git", "push", "-u", "origin", branch); err != nil {
		updateComment(formatError("Failed to push branch", err))
		return
	}

	prTitle := fmt.Sprintf("Fix #%d: %s", num, title)
	prBody := fmt.Sprintf("Closes #%d\n\nImplemented automatically by Claude.", num)
	prURL, err := runCmd(worktreeDir, gitTimeout, "gh", "pr", "create", "--title", prTitle, "--body", prBody, "--repo", repo)
	if err != nil {
		updateComment(formatError("Failed to create PR", err))
		return
	}

	prURL = strings.TrimSpace(prURL)
	deleteSpinner()
	postIssueComment(repo, repoDir, num, fmt.Sprintf("PR created: %s", prURL))
	success = true

	log.Printf("[%s] PR created for issue #%d: %s", repo, num, prURL)
}

// runCmdWithStdin executes a command with stdin input.
func runCmdWithStdin(dir string, timeout time.Duration, stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
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

// runClaudeStreaming runs claude with stream-json output, calling onUpdate periodically
// with accumulated text so callers can show live progress.
func runClaudeStreaming(dir string, timeout time.Duration, onUpdate func(partial string), prompt string) (*streamResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", prompt)
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	var accumulated strings.Builder
	var accMu sync.Mutex
	var res streamResult
	const maxCommentLen = 60000 // GitHub limit is 65536, leave margin

	// Ticker-based updates every 2 seconds. Since the SVG spinner animates
	// natively, we only update the comment when partial text actually changes.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	done := make(chan struct{})
	var lastPartial string

	go func() {
		for {
			select {
			case <-ticker.C:
				accMu.Lock()
				partial := accumulated.String()
				accMu.Unlock()
				if partial == lastPartial {
					continue // no new text, skip update
				}
				lastPartial = partial
				if len(partial) > maxCommentLen {
					partial = partial[len(partial)-maxCommentLen:]
				}
				onUpdate(partial)
			case <-done:
				return
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1<<20) // 1MB max line size

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var evt streamEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue // skip malformed lines
		}

		switch evt.Type {
		case "assistant":
			if evt.Message != nil {
				for _, c := range evt.Message.Content {
					if c.Type == "text" && c.Text != "" {
						accMu.Lock()
						accumulated.WriteString(c.Text)
						accMu.Unlock()
					}
				}
			}
		case "result":
			res.TotalCostUSD = evt.TotalCostUSD
			res.DurationMS = evt.DurationMS
			res.NumTurns = evt.NumTurns
			if evt.Result != "" {
				res.Text = evt.Result
			}
		}
	}

	close(done)
	waitErr := cmd.Wait()
	elapsed := time.Since(start)
	log.Printf("  claude -p done (%s)", elapsed.Round(time.Millisecond))

	// Use accumulated text if result event didn't provide final text.
	if res.Text == "" {
		res.Text = accumulated.String()
	}

	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("claude -p: timed out after %s", timeout)
	}
	if waitErr != nil {
		// If we got some text, return it with the error for context.
		if res.Text != "" {
			return &res, fmt.Errorf("claude -p: %w\nstderr: %s", waitErr, stderr.String())
		}
		return nil, fmt.Errorf("claude -p: %w\nstderr: %s", waitErr, stderr.String())
	}

	return &res, nil
}

// formatMetadataFooter returns a markdown footer with run metadata.
// progressBody formats the in-progress comment with SVG spinner and partial output.
func progressBody(action, partial string) string {
	header := fmt.Sprintf("🤖 %s\n\n%s", action, spinnerImg)
	if partial == "" {
		return header
	}
	return header + "\n\n" + partial
}

func formatMetadataFooter(r *streamResult) string {
	secs := r.DurationMS / 1000
	return fmt.Sprintf("\n\n---\n⏱️ %ds | 💰 $%.4f | 🔄 %d turn(s)", secs, r.TotalCostUSD, r.NumTurns)
}

// branchExists checks if a git branch exists without noisy logging.
func branchExists(dir, branch string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", branch)
	cmd.Dir = dir
	return cmd.Run() == nil
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

// postProgressComment posts a "working on it" placeholder and returns an
// update function (to replace the comment body) and a delete function (to
// remove the comment entirely). If the placeholder fails, delete is a no-op
// and the updater falls back to posting a new comment.
func postProgressComment(repo, repoDir string, num int, placeholder string) (update func(string), delete func()) {
	// Create the placeholder comment and capture its ID.
	out, err := runCmd(repoDir, gitTimeout, "gh", "api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, num),
		"-X", "POST",
		"-f", "body="+placeholder,
		"--jq", ".id")
	if err != nil {
		log.Printf("[%s#%d] failed to post progress comment: %v", repo, num, err)
		// Return a fallback updater and no-op deleter.
		return func(body string) {
			postIssueComment(repo, repoDir, num, body)
		}, func() {}
	}

	commentID := strings.TrimSpace(out)
	log.Printf("[%s#%d] progress comment created: %s", repo, num, commentID)

	update = func(body string) {
		// Use --input - to pass body via stdin, avoiding shell escaping issues with multiline content.
		jsonBody, _ := json.Marshal(map[string]string{"body": body})
		_, err := runCmdWithStdin(repoDir, gitTimeout, string(jsonBody), "gh", "api",
			fmt.Sprintf("repos/%s/issues/comments/%s", repo, commentID),
			"-X", "PATCH",
			"--input", "-")
		if err != nil {
			log.Printf("[%s#%d] failed to update comment %s, posting new: %v", repo, num, commentID, err)
			// Delete the stale placeholder to avoid duplicates.
			runCmd(repoDir, gitTimeout, "gh", "api",
				fmt.Sprintf("repos/%s/issues/comments/%s", repo, commentID),
				"-X", "DELETE")
			postIssueComment(repo, repoDir, num, body)
		}
	}

	delete = func() {
		_, err := runCmd(repoDir, gitTimeout, "gh", "api",
			fmt.Sprintf("repos/%s/issues/comments/%s", repo, commentID),
			"-X", "DELETE")
		if err != nil {
			log.Printf("[%s#%d] failed to delete progress comment %s: %v", repo, num, commentID, err)
		}
	}

	return update, delete
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
	postIssueComment(repo, repoDir, num, formatError(msg, err))
}

// formatError creates a sanitized error message for GitHub comments.
func formatError(msg string, err error) string {
	sanitized := sanitizeError(err.Error())
	return fmt.Sprintf("**Error**: %s\n\n```\n%s\n```", msg, sanitized)
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
