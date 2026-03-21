# Repo Owner Auto-Approve Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Allow all repository owners/admins to use `@claude approve` without being hardcoded in `ALLOWED_USERS`.

**Architecture:** Add a GitHub API permission check as a fallback when a user is not in `ALLOWED_USERS`. Use `gh api repos/{owner}/{repo}/collaborators/{username}/permission` to check if the commenting user has `admin` or `maintain` permission on the repo. Cache results briefly to avoid excessive API calls.

**Tech Stack:** Go, GitHub CLI (`gh api`), existing `runCmd` helper

---

### Task 1: Add `isUserAllowed` function with GitHub API permission check

**Files:**
- Modify: `main.go:500-516` (extract auth check into new function)

**Step 1: Write the failing test**

Add to `main_test.go`:

```go
func TestIsUserAllowed(t *testing.T) {
	cfg := &Config{
		AllowedUsers: map[string]bool{"alice": true},
	}

	// Allowlisted user is always allowed
	if !isUserAllowed(cfg, "owner/repo", "/tmp", "alice") {
		t.Error("allowlisted user should be allowed")
	}

	// Non-allowlisted user falls through to GitHub API check
	// (will fail in test env since gh is not configured, but function should not panic)
	result := isUserAllowed(cfg, "owner/repo", "/tmp", "unknown-user-xyz")
	// In test env without gh, this should return false (API call fails gracefully)
	if result {
		t.Error("unknown user without gh access should not be allowed")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/htlin/claude-with-webhook && go test -run TestIsUserAllowed -v`
Expected: FAIL - `isUserAllowed` not defined

**Step 3: Implement `isUserAllowed` function**

Add to `main.go` before `classifyComment`:

```go
// isUserAllowed checks if a user is authorized: first from AllowedUsers config,
// then by querying GitHub for repo collaborator permission (admin or maintain).
func isUserAllowed(cfg *Config, repo, repoDir, username string) bool {
	if cfg.AllowedUsers[username] {
		return true
	}

	// Query GitHub API for collaborator permission level
	out, err := runCmd(repoDir, gitTimeout, "gh", "api",
		fmt.Sprintf("repos/%s/collaborators/%s/permission", repo, username),
		"--jq", ".permission")
	if err != nil {
		log.Printf("[%s] failed to check permission for %s: %v", repo, username, err)
		return false
	}

	perm := strings.TrimSpace(out)
	if perm == "admin" || perm == "maintain" || perm == "write" {
		log.Printf("[%s] user %s authorized via GitHub permission: %s", repo, username, perm)
		return true
	}

	log.Printf("[%s] user %s has insufficient permission: %s", repo, username, perm)
	return false
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/htlin/claude-with-webhook && go test -run TestIsUserAllowed -v`
Expected: PASS

**Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add isUserAllowed with GitHub API permission fallback"
```

---

### Task 2: Replace inline `AllowedUsers` checks with `isUserAllowed`

**Files:**
- Modify: `main.go:453` (in `handleIssueOpened`)
- Modify: `main.go:505-516` (in `classifyComment` - add `repo` and `repoDir` params)
- Modify: `main.go:545` (call site of `classifyComment` in `handleIssueComment`)

**Step 1: Update `classifyComment` signature to accept `repo` and `repoDir`**

Change signature from:
```go
func classifyComment(cfg *Config, sender, senderType, body string) string {
```
To:
```go
func classifyComment(cfg *Config, repo, repoDir, sender, senderType, body string) string {
```

Replace the inline check:
```go
if !cfg.AllowedUsers[sender] {
    return "skip-user"
}
```
With:
```go
if !isUserAllowed(cfg, repo, repoDir, sender) {
    return "skip-user"
}
```

**Step 2: Update `handleIssueOpened` to use `isUserAllowed`**

Change line 453 from:
```go
if !cfg.AllowedUsers[sender] {
```
To:
```go
if !isUserAllowed(cfg, repo, repoDir, sender) {
```

**Step 3: Update call site in `handleIssueComment`**

Change line 545 from:
```go
action := classifyComment(cfg, p.Comment.User.Login, p.Sender.Type, p.Comment.Body)
```
To:
```go
action := classifyComment(cfg, repo, repoDir, p.Comment.User.Login, p.Sender.Type, p.Comment.Body)
```

**Step 4: Update all existing tests for `classifyComment`**

All test calls to `classifyComment` need the new `repo, repoDir` params. Add `"test/repo", "/tmp"` as the 2nd and 3rd args to every call. For example:

```go
// Before:
got := classifyComment(tt.cfg, tt.sender, tt.senderType, tt.body)
// After:
got := classifyComment(tt.cfg, "test/repo", "/tmp", tt.sender, tt.senderType, tt.body)
```

Do this for all `classifyComment` calls in tests.

**Step 5: Run all tests**

Run: `cd /Users/htlin/claude-with-webhook && go test -v`
Expected: ALL PASS

**Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "refactor: replace hardcoded AllowedUsers checks with isUserAllowed"
```

---

### Task 3: Make `ALLOWED_USERS` optional in config

**Files:**
- Modify: `main.go:291-297` (in `loadConfig`)
- Modify: `.env.example`
- Modify: `README.md` (document the new behavior)

**Step 1: Update `.env.example`**

Change:
```
ALLOWED_USERS=htlin222
```
To:
```
# ALLOWED_USERS=user1,user2  (optional: repo owners/admins/writers are auto-allowed)
```

**Step 2: Update README.md**

Find the section documenting `ALLOWED_USERS` and add a note:

> `ALLOWED_USERS` is now optional. If omitted, all repository collaborators with `write`, `maintain`, or `admin` permission are automatically allowed to use `@claude` commands. You can still set `ALLOWED_USERS` to restrict access to a specific set of users.

**Step 3: Verify the server starts without `ALLOWED_USERS`**

Run: `cd /Users/htlin/claude-with-webhook && go build -o /dev/null .`
Expected: Compiles cleanly

**Step 4: Commit**

```bash
git add main.go .env.example README.md
git commit -m "docs: make ALLOWED_USERS optional, document GitHub permission fallback"
```

---

## Summary of Changes

| What | Before | After |
|------|--------|-------|
| Auth check | `cfg.AllowedUsers[sender]` (hardcoded map) | `isUserAllowed()` - checks map first, then GitHub API |
| `ALLOWED_USERS` env | Required | Optional (fallback to GitHub permissions) |
| Who can approve | Only users in `.env` | Any repo collaborator with write+ permission |
| API call | None | `gh api repos/{owner}/{repo}/collaborators/{username}/permission` |

## Risks & Mitigations

- **Rate limiting**: GitHub API is called per-comment from non-allowlisted users. Acceptable since webhook volume is low. Could add caching later if needed.
- **Auth scope**: The `gh` CLI must have a token with `repo` scope to read collaborator permissions. This is typically already the case for webhook setups.
- **Fail-closed**: If the API call fails, the user is denied (not approved). This is the safe default.
