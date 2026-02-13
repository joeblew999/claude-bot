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

type Config struct {
	Repos          []string
	PollInterval   time.Duration
	Workers        int
	IssueLabel     string
	WIPLabel       string
	DoneLabel      string
	NeedsInfoLabel string
	FailedLabel    string
	TriageLabel    string
	Triage         bool
	WorktreeDir    string
	RepoDir        string
	LogDir         string
	MaxTurns       int
	MaxRetries     int
}

func loadConfig() Config {
	// Load .env file if present (no external deps, just parse KEY=VALUE lines)
	loadDotEnv()

	cfg := Config{
		PollInterval:   30 * time.Second,
		Workers:        3,
		IssueLabel:     "todo",
		WIPLabel:       "in-progress",
		DoneLabel:      "done",
		NeedsInfoLabel: "needs-info",
		FailedLabel:    "failed",
		TriageLabel:    "triaged",
		Triage:         false,
		WorktreeDir:    expandHome("~/.claude-bot/trees"),
		RepoDir:        expandHome("~/.claude-bot/repos"),
		LogDir:         expandHome("~/.claude-bot/logs"),
		MaxTurns:       50,
		MaxRetries:     3,
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
	if v := os.Getenv("CB_NEEDS_INFO_LABEL"); v != "" {
		cfg.NeedsInfoLabel = v
	}
	if v := os.Getenv("CB_FAILED_LABEL"); v != "" {
		cfg.FailedLabel = v
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
	if v := os.Getenv("CB_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxRetries = n
		}
	}
	if v := os.Getenv("CB_TRIAGE_LABEL"); v != "" {
		cfg.TriageLabel = v
	}
	if os.Getenv("CB_TRIAGE") == "1" {
		cfg.Triage = true
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
		// Upgrade gh to latest if auto-install is on
		if autoInstall {
			upgradePackage(pm, "gh")
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

// upgradePackage upgrades an already-installed package to latest.
// Idempotent: no-op if already at latest version.
func upgradePackage(pm, pkg string) {
	var cmd *exec.Cmd
	switch pm {
	case "brew":
		cmd = exec.Command("brew", "upgrade", pkg)
	case "apt":
		cmd = exec.Command("sudo", "apt-get", "install", "--only-upgrade", "-y", pkg)
	case "dnf":
		cmd = exec.Command("sudo", "dnf", "upgrade", "-y", pkg)
	default:
		return
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: couldn't upgrade %s: %v", pkg, err)
	}
}

// installNpm installs a global package. Prefers bun, falls back to npm.
func installNpm(pkg string) {
	if _, err := exec.LookPath("bun"); err == nil {
		log.Printf("installing %s via bun...", pkg)
		cmd := exec.Command("bun", "install", "-g", pkg)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			log.Printf("installed %s via bun", pkg)
			return
		}
		log.Printf("bun failed, falling back to npm")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		log.Fatalf("cannot auto-install %s: neither bun nor npm found", pkg)
	}
	log.Printf("installing %s via npm...", pkg)
	cmd := exec.Command("npm", "install", "-g", pkg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to install %s: %v", pkg, err)
	}
	log.Printf("installed %s via npm", pkg)
}

// --- Main ---

func main() {
	cfg := loadConfig()

	// Handle subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--clean":
			cleanState(cfg)
			return
		case "--clean-all":
			cleanEverything(cfg)
			return
		case "--build":
			buildSelf()
			return
		case "--update":
			selfUpdate()
			return
		case "--release":
			selfRelease()
			return
		}
	}

	if len(cfg.Repos) == 0 {
		log.Fatal("CB_REPOS environment variable is required (comma-separated list of owner/repo)")
	}

	// Idempotent dependency check — verifies required tools are installed and configured
	checkDependencies()

	ensureDirs(cfg)

	log.Printf("claude-bot starting: repos=%v workers=%d poll=%s retries=%d",
		cfg.Repos, cfg.Workers, cfg.PollInterval, cfg.MaxRetries)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Startup: create labels if missing, recover stale issues
	ensureLabels(ctx, cfg)
	recoverStaleIssues(ctx, cfg)

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

		// Triage: find unlabeled issues and respond
		if cfg.Triage {
			triageNewIssues(ctx, cfg, repo)
		}

		issues, err := fetchIssues(ctx, repo, cfg.IssueLabel)
		if err != nil {
			log.Printf("[poll] error fetching issues from %s: %v", repo, err)
			continue
		}

		for _, issue := range issues {
			// Skip if already has WIP, done, or failed label
			if issue.hasLabel(cfg.WIPLabel) || issue.hasLabel(cfg.DoneLabel) || issue.hasLabel(cfg.FailedLabel) {
				continue
			}

			// Retry limit: if too many bot errors, mark as failed and skip
			if errors := countBotErrors(issue); errors >= cfg.MaxRetries {
				log.Printf("[poll] %s has failed %d times (max %d), marking as failed", issue.key(), errors, cfg.MaxRetries)
				_ = addLabel(ctx, issue, cfg.FailedLabel)
				_ = removeLabel(ctx, issue, cfg.IssueLabel)
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
	args := []string{"issue", "list", "--repo", repo,
		"--json", "number,title,body,labels,url,comments,author",
		"--limit", "50",
	}
	if label != "" {
		args = append(args, "--label", label)
	}

	out, err := run(ctx, "", "gh", args...)
	if err != nil {
		return nil, err
	}

	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parsing issues JSON: %w", err)
	}

	for i := range issues {
		issues[i].Repo = repo
	}

	return issues, nil
}

// --- Triage ---

// triageNewIssues finds open issues with no bot labels and posts a friendly response.
// Idempotent: adds TriageLabel after responding so we don't re-triage.
func triageNewIssues(ctx context.Context, cfg Config, repo string) {
	issues, err := fetchIssues(ctx, repo, "")
	if err != nil {
		log.Printf("[triage] error fetching issues from %s: %v", repo, err)
		return
	}

	botLabels := []string{
		cfg.IssueLabel, cfg.WIPLabel, cfg.DoneLabel,
		cfg.NeedsInfoLabel, cfg.FailedLabel, cfg.TriageLabel,
	}

	for _, issue := range issues {
		// Skip if issue already has any bot label
		hasBotLabel := false
		for _, bl := range botLabels {
			if issue.hasLabel(bl) {
				hasBotLabel = true
				break
			}
		}
		if hasBotLabel {
			continue
		}

		// Skip if bot already commented
		if hasBotComment(issue) {
			_ = addLabel(ctx, issue, cfg.TriageLabel)
			continue
		}

		log.Printf("[triage] responding to %s: %q", issue.key(), issue.Title)

		response := buildTriageResponse(ctx, issue)
		if err := commentOnIssue(ctx, issue, response); err != nil {
			log.Printf("[triage] error commenting on %s: %v", issue.key(), err)
			continue
		}
		_ = addLabel(ctx, issue, cfg.TriageLabel)
	}
}

// hasBotComment checks if claude-bot has already commented on the issue.
func hasBotComment(issue Issue) bool {
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, "claude-bot") {
			return true
		}
	}
	return false
}

// buildTriageResponse uses Claude CLI to generate a context-aware, human-sounding
// triage response based on the issue content. Falls back to a simple response if Claude fails.
func buildTriageResponse(ctx context.Context, issue Issue) string {
	prompt := fmt.Sprintf(`You are responding to a GitHub issue as a helpful project contributor.
Write a short, friendly, human-sounding reply. DO NOT say you are a bot. DO NOT use phrases like "I'm an AI" or "automated".
Just respond naturally as if you're a person on the team who read their issue.

- If they asked a question, try to help or point them in the right direction.
- If they reported a bug, acknowledge it and ask for more details if the report is thin.
- If they requested a feature, acknowledge the idea.
- Keep it to 2-4 sentences max. Be warm but concise.
- End by letting them know a maintainer will look at this soon, and if it's something actionable, it can be picked up for work.

Issue title: %s
Issue author: @%s
Issue body:
%s`, issue.Title, issue.Author.Login, issue.Body)

	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--max-turns", "1")
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[triage] claude failed, using fallback: %v", err)
		return fmt.Sprintf("Hey @%s, thanks for raising this! A maintainer will take a look soon.", issue.Author.Login)
	}

	return strings.TrimSpace(string(out))
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

	// Step 6: No changes → needs more info from user
	if !hasChanges {
		_ = commentOnIssue(ctx, issue, "claude-bot ran but couldn't resolve this issue — no file changes were made.\n\nPlease add more context or details as a comment, then replace the `"+cfg.NeedsInfoLabel+"` label with `"+cfg.IssueLabel+"` to retry.")
		_ = removeLabel(ctx, issue, cfg.WIPLabel)
		_ = addLabel(ctx, issue, cfg.NeedsInfoLabel)
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

// loadDotEnv reads .env from the binary's directory. No-op if missing.
// Stdlib only — no external deps. Only sets vars not already in the environment.
func loadDotEnv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// Don't override existing env vars
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
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

// ensureLabels creates the required labels on each repo if they don't exist.
// Idempotent: gh label create errors if label already exists, which we ignore.
func ensureLabels(ctx context.Context, cfg Config) {
	labels := []struct{ name, color, desc string }{
		{cfg.IssueLabel, "0E8A16", "Issue ready for claude-bot"},
		{cfg.WIPLabel, "FBCA04", "claude-bot is working on this"},
		{cfg.DoneLabel, "1D76DB", "claude-bot created a PR"},
		{cfg.NeedsInfoLabel, "D93F0B", "claude-bot needs more context"},
		{cfg.FailedLabel, "B60205", "claude-bot failed after max retries"},
		{cfg.TriageLabel, "C5DEF5", "claude-bot triaged this issue"},
	}
	for _, repo := range cfg.Repos {
		for _, l := range labels {
			if _, err := run(ctx, "", "gh", "label", "create", l.name,
				"--repo", repo,
				"--color", l.color,
				"--description", l.desc,
			); err != nil {
				continue // Label already exists — fine
			}
			log.Printf("[labels] created %q on %s", l.name, repo)
		}
	}
}

// recoverStaleIssues resets issues stuck in "in-progress" with no PR back to "todo".
// Called on startup before workers start, so there's no race condition.
func recoverStaleIssues(ctx context.Context, cfg Config) {
	for _, repo := range cfg.Repos {
		issues, err := fetchIssues(ctx, repo, cfg.WIPLabel)
		if err != nil {
			log.Printf("[recovery] error checking stale issues in %s: %v", repo, err)
			continue
		}
		for _, issue := range issues {
			branch := branchName(issue)
			// Check if a PR already exists
			out, _ := run(ctx, "", "gh", "pr", "list",
				"--repo", issue.Repo,
				"--head", branch,
				"--json", "url",
				"--limit", "1",
			)
			var prs []struct{ URL string `json:"url"` }
			if json.Unmarshal([]byte(out), &prs) == nil && len(prs) > 0 {
				// PR exists — mark done
				log.Printf("[recovery] %s has PR, marking done", issue.key())
				_ = addLabel(ctx, issue, cfg.DoneLabel)
				_ = removeLabel(ctx, issue, cfg.WIPLabel)
				continue
			}
			// No PR — reset to todo
			log.Printf("[recovery] %s stuck in-progress with no PR, resetting to todo", issue.key())
			_ = removeLabel(ctx, issue, cfg.WIPLabel)
			_ = addLabel(ctx, issue, cfg.IssueLabel)
		}
	}
}

// countBotErrors counts how many error comments the bot has posted on an issue.
func countBotErrors(issue Issue) int {
	count := 0
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, "claude-bot encountered an error:") {
			count++
		}
	}
	return count
}
