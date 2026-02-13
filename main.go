// claude-bot: watches GitHub repos for todo-labeled issues, runs Claude Code, creates PRs.
// Single binary, pure Go, stdlib only, fully idempotent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// --- Config ---
// All env vars are prefixed with CB_ to avoid clashes with other tools.

const defaultGreeting = "Hey there! Thanks for opening this issue — I'm on it. I'll have a PR ready for review shortly. Hang tight!"

// greetingSignature is appended to greeting comments so the bot can identify its own greetings.
const greetingSignature = "\n\n---\n*🤖 claude-bot is working on this*"

type Config struct {
	Repos        []string
	PollInterval time.Duration
	Workers      int
	IssueLabel   string
	WIPLabel     string
	DoneLabel    string
	WorktreeDir  string
	RepoDir      string
	LogDir       string
	MaxTurns     int
	Greeting     string
}

func loadConfig() Config {
	cfg := Config{
		PollInterval: 30 * time.Second,
		Workers:      3,
		IssueLabel:   "todo",
		WIPLabel:     "in-progress",
		DoneLabel:    "done",
		WorktreeDir:  expandHome("~/.claude-bot/trees"),
		RepoDir:      expandHome("~/.claude-bot/repos"),
		LogDir:       expandHome("~/.claude-bot/logs"),
		MaxTurns:     50,
		Greeting:     defaultGreeting,
	}

	if v := os.Getenv("CB_REPOS"); v != "" {
		for _, r := range strings.Split(v, ",") {
			r = strings.TrimSpace(r)
			if r != "" {
				cfg.Repos = append(cfg.Repos, r)
			}
		}
	}
	if v := os.Getenv("CB_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.PollInterval = d
		}
	}
	if v := os.Getenv("CB_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Workers = n
		}
	}
	if v := os.Getenv("CB_ISSUE_LABEL"); v != "" {
		cfg.IssueLabel = v
	}
	if v := os.Getenv("CB_WIP_LABEL"); v != "" {
		cfg.WIPLabel = v
	}
	if v := os.Getenv("CB_DONE_LABEL"); v != "" {
		cfg.DoneLabel = v
	}
	if v := os.Getenv("CB_WORKTREE_DIR"); v != "" {
		cfg.WorktreeDir = expandHome(v)
	}
	if v := os.Getenv("CB_REPO_DIR"); v != "" {
		cfg.RepoDir = expandHome(v)
	}
	if v := os.Getenv("CB_LOG_DIR"); v != "" {
		cfg.LogDir = expandHome(v)
	}
	if v := os.Getenv("CB_MAX_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxTurns = n
		}
	}
	if v := os.Getenv("CB_GREETING"); v != "" {
		cfg.Greeting = v
	}

	return cfg
}

// --- Types ---

type Issue struct {
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Repo     string    `json:"-"`
	Labels   []Label   `json:"labels"`
	URL      string    `json:"url"`
	Comments []Comment `json:"comments"`
	Author   struct {
		Login string `json:"login"`
	} `json:"author"`
}

type Label struct {
	Name string `json:"name"`
}

type Comment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

func (i Issue) hasLabel(name string) bool {
	for _, l := range i.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

func (i Issue) key() string {
	return fmt.Sprintf("%s#%d", i.Repo, i.Number)
}

// --- Tracker (in-memory dedup) ---

type tracker struct {
	mu       sync.Mutex
	inflight map[string]bool
}

func newTracker() *tracker {
	return &tracker{inflight: make(map[string]bool)}
}

func (t *tracker) tryAcquire(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inflight[key] {
		return false
	}
	t.inflight[key] = true
	return true
}

func (t *tracker) release(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inflight, key)
}

// --- Dependency Check ---

// checkDependencies verifies all required external tools are installed and configured.
// With CB_AUTO_INSTALL=1, it will attempt to install missing tools automatically.
// Idempotent: skips tools that are already installed.
func checkDependencies() {
	autoInstall := os.Getenv("CB_AUTO_INSTALL") == "1"

	// Detect package manager (idempotent — just reads state)
	var pm string
	if autoInstall {
		pm = detectPackageManager()
		if pm == "" {
			log.Fatal("CB_AUTO_INSTALL=1 but no supported package manager found (need brew, apt, or dnf)")
		}
		log.Printf("auto-install enabled, using %s", pm)
	}

	var manual []string

	// --- git ---
	if _, err := exec.LookPath("git"); err != nil {
		if autoInstall {
			installPackage(pm, "git")
		} else {
			manual = append(manual, "git (install from https://git-scm.com)")
		}
	}
	// Verify git identity (can't auto-install — user must configure)
	if _, err := exec.LookPath("git"); err == nil {
		name, _ := exec.Command("git", "config", "user.name").Output()
		email, _ := exec.Command("git", "config", "user.email").Output()
		if strings.TrimSpace(string(name)) == "" || strings.TrimSpace(string(email)) == "" {
			manual = append(manual, "git user identity (run: git config --global user.name 'You' && git config --global user.email 'you@example.com')")
		}
	}

	// --- gh (GitHub CLI) ---
	if _, err := exec.LookPath("gh"); err != nil {
		if autoInstall {
			installPackage(pm, "gh")
		} else {
			manual = append(manual, "gh (install from https://cli.github.com)")
		}
	}
	// Verify gh auth (can't auto-install — user must authenticate)
	if _, err := exec.LookPath("gh"); err == nil {
		if err := exec.Command("gh", "auth", "status").Run(); err != nil {
			manual = append(manual, "gh auth (run: gh auth login)")
		}
	}

	// --- claude (Claude Code CLI) ---
	if _, err := exec.LookPath("claude"); err != nil {
		if autoInstall {
			installNpm("@anthropic-ai/claude-code")
		} else {
			manual = append(manual, "claude (install: npm install -g @anthropic-ai/claude-code)")
		}
	}

	if len(manual) > 0 {
		log.Fatalf("missing required dependencies (set CB_AUTO_INSTALL=1 to auto-install where possible):\n  - %s", strings.Join(manual, "\n  - "))
	}

	log.Println("dependency check passed: git, gh, claude all available")
}

// detectPackageManager returns the available package manager, or "" if none found.
func detectPackageManager() string {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			return "brew"
		}
	case "linux":
		if _, err := exec.LookPath("apt-get"); err == nil {
			return "apt"
		}
		if _, err := exec.LookPath("dnf"); err == nil {
			return "dnf"
		}
		if _, err := exec.LookPath("brew"); err == nil {
			return "brew"
		}
	}
	return ""
}

// installPackage installs a system package via the detected package manager.
// Idempotent: the package manager itself skips already-installed packages.
func installPackage(pm, pkg string) {
	log.Printf("installing %s via %s...", pkg, pm)
	var cmd *exec.Cmd
	switch pm {
	case "brew":
		cmd = exec.Command("brew", "install", pkg)
	case "apt":
		cmd = exec.Command("sudo", "apt-get", "install", "-y", pkg)
	case "dnf":
		cmd = exec.Command("sudo", "dnf", "install", "-y", pkg)
	default:
		log.Fatalf("unsupported package manager: %s", pm)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to install %s: %v", pkg, err)
	}
	log.Printf("installed %s successfully", pkg)
}

// installNpm installs a global npm package.
// Idempotent: npm skips if already installed at latest version.
func installNpm(pkg string) {
	// Need npm to install
	if _, err := exec.LookPath("npm"); err != nil {
		log.Fatalf("cannot auto-install %s: npm not found (install Node.js first)", pkg)
	}
	log.Printf("installing %s via npm...", pkg)
	cmd := exec.Command("npm", "install", "-g", pkg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to install %s: %v", pkg, err)
	}
	log.Printf("installed %s successfully", pkg)
}

// --- Main ---

func main() {
	cfg := loadConfig()

	// Handle clean commands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--clean":
			cleanState(cfg)
			return
		case "--clean-all":
			cleanEverything(cfg)
			return
		}
	}

	if len(cfg.Repos) == 0 {
		log.Fatal("CB_REPOS environment variable is required (comma-separated list of owner/repo)")
	}

	// Idempotent dependency check — verifies required tools are installed and configured
	checkDependencies()

	ensureDirs(cfg)

	log.Printf("claude-bot starting: repos=%v workers=%d poll=%s",
		cfg.Repos, cfg.Workers, cfg.PollInterval)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	jobs := make(chan Issue, 100)
	t := newTracker()
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			worker(ctx, cfg, id, jobs, t)
		}(i)
	}

	// Poll loop
	pollLoop(ctx, cfg, jobs, t)

	close(jobs)
	log.Println("waiting for workers to finish...")
	wg.Wait()
	log.Println("claude-bot stopped")
}

// --- Poll Loop ---

func pollLoop(ctx context.Context, cfg Config, jobs chan<- Issue, t *tracker) {
	// Immediate first poll
	poll(ctx, cfg, jobs, t)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll(ctx, cfg, jobs, t)
		}
	}
}

func poll(ctx context.Context, cfg Config, jobs chan<- Issue, t *tracker) {
	for _, repo := range cfg.Repos {
		if ctx.Err() != nil {
			return
		}

		issues, err := fetchIssues(ctx, repo, cfg.IssueLabel)
		if err != nil {
			log.Printf("[poll] error fetching issues from %s: %v", repo, err)
			continue
		}

		for _, issue := range issues {
			// Skip if already has WIP or done label
			if issue.hasLabel(cfg.WIPLabel) || issue.hasLabel(cfg.DoneLabel) {
				continue
			}

			// Skip if already inflight
			if !t.tryAcquire(issue.key()) {
				continue
			}

			select {
			case jobs <- issue:
				log.Printf("[poll] queued %s: %q", issue.key(), issue.Title)
			case <-ctx.Done():
				t.release(issue.key())
				return
			}
		}
	}
}

func fetchIssues(ctx context.Context, repo, label string) ([]Issue, error) {
	out, err := run(ctx, "", "gh", "issue", "list",
		"--repo", repo,
		"--label", label,
		"--json", "number,title,body,labels,url,comments,author",
		"--limit", "50",
	)
	if err != nil {
		return nil, err
	}

	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parsing issues JSON: %w", err)
	}

	// Set repo on each issue
	for i := range issues {
		issues[i].Repo = repo
	}

	return issues, nil
}

// --- Worker ---

func worker(ctx context.Context, cfg Config, id int, jobs <-chan Issue, t *tracker) {
	for issue := range jobs {
		if ctx.Err() != nil {
			t.release(issue.key())
			return
		}

		log.Printf("[worker-%d] picked up %s: %q", id, issue.key(), issue.Title)

		if err := processIssue(ctx, cfg, id, issue); err != nil {
			log.Printf("[worker-%d] error processing %s: %v", id, issue.key(), err)
		}

		t.release(issue.key())
	}
}

func processIssue(ctx context.Context, cfg Config, workerID int, issue Issue) (retErr error) {
	branch := branchName(issue)
	repoDir := filepath.Join(cfg.RepoDir, issue.Repo)
	wtDir := filepath.Join(cfg.WorktreeDir, issue.Repo, branch)
	logFile := filepath.Join(cfg.LogDir, fmt.Sprintf("%s-%d.log", slugify(issue.Repo), issue.Number))

	// On failure: comment error on issue (deduped), reset labels, cleanup
	defer func() {
		if retErr != nil {
			commentErr := fmt.Sprintf("claude-bot encountered an error:\n```\n%s\n```\nNeeds manual attention.", retErr.Error())
			// Only comment if we haven't already posted this exact error
			if !lastCommentContains(ctx, issue, retErr.Error()) {
				_ = commentOnIssue(ctx, issue, commentErr)
			}
			_ = removeLabel(ctx, issue, cfg.WIPLabel)
			_ = addLabel(ctx, issue, cfg.IssueLabel)
			cleanupWorktree(ctx, repoDir, wtDir, branch)
		}
	}()

	// Step 1: Mark in-progress (idempotent)
	if !issue.hasLabel(cfg.WIPLabel) {
		if err := addLabel(ctx, issue, cfg.WIPLabel); err != nil {
			return fmt.Errorf("marking in-progress: %w", err)
		}
		if err := removeLabel(ctx, issue, cfg.IssueLabel); err != nil {
			log.Printf("[worker-%d] warning: couldn't remove %s label: %v", workerID, cfg.IssueLabel, err)
		}
	}

	// Step 1.5: Post friendly greeting (idempotent)
	if err := greetIssue(ctx, cfg, issue); err != nil {
		log.Printf("[worker-%d] warning: couldn't post greeting on %s: %v", workerID, issue.key(), err)
	}

	// Step 2: Ensure repo cloned (idempotent)
	if err := ensureRepoCloned(ctx, cfg, issue); err != nil {
		return fmt.Errorf("cloning repo: %w", err)
	}

	// Step 3: Fetch latest
	if _, err := run(ctx, repoDir, "git", "fetch", "origin"); err != nil {
		return fmt.Errorf("fetching latest: %w", err)
	}

	// Step 4: Create worktree (idempotent)
	if err := ensureWorktree(ctx, repoDir, wtDir, branch); err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}

	// Step 5: Run Claude Code (skip if changes already present)
	hasChanges, err := checkChanges(ctx, wtDir)
	if err != nil {
		return fmt.Errorf("checking changes: %w", err)
	}

	if !hasChanges {
		if err := runClaude(ctx, cfg, issue, wtDir, logFile); err != nil {
			return fmt.Errorf("running claude: %w", err)
		}

		// Re-check for changes
		hasChanges, err = checkChanges(ctx, wtDir)
		if err != nil {
			return fmt.Errorf("checking changes after claude: %w", err)
		}
	}

	// Step 6: No changes → report and cleanup
	if !hasChanges {
		_ = commentOnIssue(ctx, issue, "claude-bot ran but couldn't resolve this issue — no file changes were made. Needs manual attention.")
		_ = removeLabel(ctx, issue, cfg.WIPLabel)
		_ = addLabel(ctx, issue, cfg.IssueLabel)
		cleanupWorktree(ctx, repoDir, wtDir, branch)
		return nil // Not an error, just nothing to do
	}

	// Step 7: Commit (idempotent — skip if clean)
	if err := commitChanges(ctx, wtDir, issue); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	// Step 8: Push (idempotent)
	if _, err := run(ctx, wtDir, "git", "push", "-u", "origin", branch); err != nil {
		return fmt.Errorf("pushing: %w", err)
	}

	// Step 9: Create PR (idempotent — skip if exists)
	prURL, err := ensurePR(ctx, issue, branch, wtDir)
	if err != nil {
		return fmt.Errorf("creating PR: %w", err)
	}

	// Step 10: Comment on issue (idempotent — skip if already commented)
	if err := ensurePRComment(ctx, issue, prURL); err != nil {
		log.Printf("[worker-%d] warning: couldn't comment PR URL: %v", workerID, err)
	}

	// Step 11: Mark done (idempotent)
	if !issue.hasLabel(cfg.DoneLabel) {
		_ = addLabel(ctx, issue, cfg.DoneLabel)
		_ = removeLabel(ctx, issue, cfg.WIPLabel)
	}

	// Step 12: Cleanup worktree
	cleanupWorktree(ctx, repoDir, wtDir, branch)

	log.Printf("[worker-%d] completed %s → %s", workerID, issue.key(), prURL)
	return nil
}

// --- Idempotent Operations ---

func ensureRepoCloned(ctx context.Context, cfg Config, issue Issue) error {
	repoDir := filepath.Join(cfg.RepoDir, issue.Repo)
	gitDir := filepath.Join(repoDir, ".git")

	// Already cloned?
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		return nil
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
		return err
	}

	repoURL := fmt.Sprintf("https://github.com/%s.git", issue.Repo)
	_, err := run(ctx, "", "git", "clone", repoURL, repoDir)
	return err
}

func ensureWorktree(ctx context.Context, repoDir, wtDir, branch string) error {
	// Already exists as a worktree?
	if _, err := os.Stat(wtDir); err == nil {
		return nil
	}

	// Determine default branch
	defaultBranch := "main"
	if out, err := run(ctx, repoDir, "git", "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		parts := strings.Split(strings.TrimSpace(out), "/")
		if len(parts) > 0 {
			defaultBranch = parts[len(parts)-1]
		}
	}

	// Check if branch already exists remotely
	if _, err := run(ctx, repoDir, "git", "rev-parse", "--verify", "refs/remotes/origin/"+branch); err == nil {
		// Branch exists remotely — delete stale local branch if any, then check it out
		_ = deleteLocalBranch(ctx, repoDir, branch)
		_, err := run(ctx, repoDir, "git", "worktree", "add", wtDir, branch)
		return err
	}

	// Delete stale local branch if it exists (left over from a previous failed run)
	_ = deleteLocalBranch(ctx, repoDir, branch)

	// Create new worktree with new branch from origin/defaultBranch
	_, err := run(ctx, repoDir, "git", "worktree", "add", "-b", branch, wtDir, "origin/"+defaultBranch)
	return err
}

// deleteLocalBranch removes a local branch if it exists.
// Idempotent: silently succeeds if branch doesn't exist.
func deleteLocalBranch(ctx context.Context, repoDir, branch string) error {
	// Check if branch exists locally
	if _, err := run(ctx, repoDir, "git", "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		return nil // Branch doesn't exist, nothing to do
	}
	_, err := run(ctx, repoDir, "git", "branch", "-D", branch)
	return err
}

func checkChanges(ctx context.Context, wtDir string) (bool, error) {
	out, err := run(ctx, wtDir, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func commitChanges(ctx context.Context, wtDir string, issue Issue) error {
	// Check if there's anything to commit
	out, err := run(ctx, wtDir, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil // Already clean, nothing to commit
	}

	if _, err := run(ctx, wtDir, "git", "add", "-A"); err != nil {
		return err
	}

	msg := fmt.Sprintf("fix: resolve #%d — %s", issue.Number, issue.Title)
	_, err = run(ctx, wtDir, "git", "commit", "-m", msg)
	return err
}

func ensurePR(ctx context.Context, issue Issue, branch, wtDir string) (string, error) {
	// Check if PR already exists for this branch
	out, err := run(ctx, "", "gh", "pr", "list",
		"--repo", issue.Repo,
		"--head", branch,
		"--json", "url",
		"--limit", "1",
	)
	if err == nil {
		var prs []struct{ URL string `json:"url"` }
		if json.Unmarshal([]byte(out), &prs) == nil && len(prs) > 0 {
			return prs[0].URL, nil // PR already exists
		}
	}

	// Get diff stat for PR body
	diffStat, _ := run(ctx, wtDir, "git", "diff", "--stat", "HEAD~1")

	body := fmt.Sprintf("Closes #%d\n\n## What changed\n```\n%s\n```\n\n## Issue\n%s\n\n---\n*Automated by claude-bot. Review before merging.*",
		issue.Number, diffStat, issue.URL)

	title := fmt.Sprintf("fix: resolve #%d — %s", issue.Number, issue.Title)

	prOut, err := run(ctx, "", "gh", "pr", "create",
		"--repo", issue.Repo,
		"--title", title,
		"--body", body,
		"--head", branch,
		"--base", "main",
	)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(prOut), nil
}

func ensurePRComment(ctx context.Context, issue Issue, prURL string) error {
	// Fetch latest comments to check if we already commented
	issues, err := fetchIssues(ctx, issue.Repo, "")
	if err == nil {
		for _, iss := range issues {
			if iss.Number == issue.Number {
				for _, c := range iss.Comments {
					if strings.Contains(c.Body, prURL) {
						return nil // Already commented
					}
				}
			}
		}
	}

	return commentOnIssue(ctx, issue, fmt.Sprintf("PR ready for review: %s", prURL))
}

func runClaude(ctx context.Context, cfg Config, issue Issue, wtDir, logFile string) error {
	prompt := buildPrompt(issue)

	// Create a context with 10-minute timeout
	claudeCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(claudeCtx, "claude", "-p", prompt,
		"--allowedTools", "Bash,Read,Write,Edit",
		"--max-turns", strconv.Itoa(cfg.MaxTurns),
	)
	cmd.Dir = wtDir

	// Clear CLAUDECODE env var so claude doesn't think it's nested
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")

	// Capture output to log file
	f, err := os.Create(logFile)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}
	defer f.Close()

	cmd.Stdout = f
	cmd.Stderr = f

	log.Printf("[claude] running on %s (log: %s)", issue.key(), logFile)

	if err := cmd.Run(); err != nil {
		// Context deadline = timeout
		if claudeCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("claude timed out after 10 minutes")
		}
		return fmt.Errorf("claude exited with error: %w", err)
	}

	return nil
}

// filterEnv returns env vars with the specified key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// --- GitHub Label/Comment Helpers ---

func addLabel(ctx context.Context, issue Issue, label string) error {
	_, err := run(ctx, "", "gh", "issue", "edit",
		strconv.Itoa(issue.Number),
		"--repo", issue.Repo,
		"--add-label", label,
	)
	return err
}

func removeLabel(ctx context.Context, issue Issue, label string) error {
	_, err := run(ctx, "", "gh", "issue", "edit",
		strconv.Itoa(issue.Number),
		"--repo", issue.Repo,
		"--remove-label", label,
	)
	return err
}

func commentOnIssue(ctx context.Context, issue Issue, body string) error {
	_, err := run(ctx, "", "gh", "issue", "comment",
		strconv.Itoa(issue.Number),
		"--repo", issue.Repo,
		"--body", body,
	)
	return err
}

// greetIssue posts a friendly greeting comment on the issue.
// Idempotent: skips if a greeting (identified by the signature) was already posted.
func greetIssue(ctx context.Context, cfg Config, issue Issue) error {
	// Check existing comments for our greeting signature
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, greetingSignature) {
			return nil // Already greeted
		}
	}
	// Also check latest comments from the API in case the issue data is stale
	if anyCommentContains(ctx, issue, greetingSignature) {
		return nil
	}

	greeting := cfg.Greeting
	if author := issue.Author.Login; author != "" {
		greeting = fmt.Sprintf("Hey @%s! %s", author, cfg.Greeting)
	}
	return commentOnIssue(ctx, issue, greeting+greetingSignature)
}

// anyCommentContains checks if any comment on an issue contains the given text.
func anyCommentContains(ctx context.Context, issue Issue, text string) bool {
	out, err := run(ctx, "", "gh", "issue", "view",
		strconv.Itoa(issue.Number),
		"--repo", issue.Repo,
		"--json", "comments",
	)
	if err != nil {
		return false
	}
	var result struct {
		Comments []Comment `json:"comments"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || len(result.Comments) == 0 {
		return false
	}
	for _, c := range result.Comments {
		if strings.Contains(c.Body, text) {
			return true
		}
	}
	return false
}

// lastCommentContains checks if the most recent comment on an issue contains the given text.
// Used to prevent duplicate error comments on retries.
func lastCommentContains(ctx context.Context, issue Issue, text string) bool {
	out, err := run(ctx, "", "gh", "issue", "view",
		strconv.Itoa(issue.Number),
		"--repo", issue.Repo,
		"--json", "comments",
	)
	if err != nil {
		return false
	}
	var result struct {
		Comments []Comment `json:"comments"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || len(result.Comments) == 0 {
		return false
	}
	last := result.Comments[len(result.Comments)-1]
	return strings.Contains(last.Body, text)
}

// cleanupWorktree removes a worktree and its local branch.
// Idempotent: skips if worktree doesn't exist.
func cleanupWorktree(ctx context.Context, repoDir, wtDir, branch string) {
	if _, err := os.Stat(wtDir); os.IsNotExist(err) {
		return
	}
	if _, err := run(ctx, repoDir, "git", "worktree", "remove", wtDir, "--force"); err != nil {
		log.Printf("[cleanup] warning: couldn't remove worktree %s: %v", wtDir, err)
	}
	// Also delete the local branch to prevent stale branch errors on retry
	_ = deleteLocalBranch(ctx, repoDir, branch)
}

// --- Command Runner ---

func run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}

	return string(out), nil
}

// --- Clean ---

// cleanState removes worktrees and logs but preserves repo clones.
// Use this to reset working state so the next run re-tests deps and starts fresh.
// Idempotent: skips directories that don't exist.
func cleanState(cfg Config) {
	removeDirs([]struct{ name, path string }{
		{"worktrees", cfg.WorktreeDir},
		{"logs", cfg.LogDir},
	})
	log.Println("[clean] done — repo clones preserved in", cfg.RepoDir)
}

// cleanEverything removes all claude-bot directories including repo clones.
// Full factory reset of ~/.claude-bot/.
// Idempotent: skips directories that don't exist.
func cleanEverything(cfg Config) {
	removeDirs([]struct{ name, path string }{
		{"worktrees", cfg.WorktreeDir},
		{"repos", cfg.RepoDir},
		{"logs", cfg.LogDir},
	})
	log.Println("[clean-all] done — full reset")
}

func removeDirs(dirs []struct{ name, path string }) {
	for _, d := range dirs {
		if _, err := os.Stat(d.path); os.IsNotExist(err) {
			log.Printf("[clean] %s: %s (not found, skipping)", d.name, d.path)
			continue
		}
		if err := os.RemoveAll(d.path); err != nil {
			log.Printf("[clean] error removing %s (%s): %v", d.name, d.path, err)
		} else {
			log.Printf("[clean] removed %s: %s", d.name, d.path)
		}
	}
}

// --- Helpers ---

func buildPrompt(issue Issue) string {
	var b strings.Builder

	b.WriteString("You are working on a codebase. Fix the following GitHub issue.\n\n")
	fmt.Fprintf(&b, "## Issue #%d: %s\n%s\n\n", issue.Number, issue.Title, issue.Body)

	if len(issue.Comments) > 0 {
		b.WriteString("## Comments (conversation with the user):\n")
		for _, c := range issue.Comments {
			fmt.Fprintf(&b, "**%s** (%s):\n%s\n\n", c.Author.Login, c.CreatedAt, c.Body)
		}
	}

	b.WriteString(`## Instructions:
- Read CLAUDE.md in the repo root for project-specific instructions
- Understand the codebase before making changes
- Make minimal, focused changes that address the issue
- Run any existing tests and make sure they pass
- If you create new functionality, add tests
- Do NOT commit — just make the file changes
`)

	return b.String()
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

func branchName(issue Issue) string {
	slug := slugify(issue.Title)
	if slug == "" {
		slug = "fix"
	}
	return fmt.Sprintf("issue-%d-%s", issue.Number, slug)
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func ensureDirs(cfg Config) {
	for _, dir := range []string{cfg.WorktreeDir, cfg.RepoDir, cfg.LogDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("failed to create directory %s: %v", dir, err)
		}
	}
}
