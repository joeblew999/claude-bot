package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// --- Unit Tests (always run, no network) ---

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Fix URI Parsing!!", "fix-uri-parsing"},
		{"add CORS headers", "add-cors-headers"},
		{"", ""},
		{"a", "a"},
		{strings.Repeat("x", 100), strings.Repeat("x", 50)},
	}
	for _, tt := range tests {
		if got := slugify(tt.in); got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBranchName(t *testing.T) {
	issue := Issue{Number: 42, Title: "Fix URI parsing"}
	if got := branchName(issue); got != "issue-42-fix-uri-parsing" {
		t.Errorf("branchName = %q", got)
	}

	// Empty title falls back to "fix"
	empty := Issue{Number: 1, Title: ""}
	if got := branchName(empty); got != "issue-1-fix" {
		t.Errorf("branchName(empty) = %q", got)
	}
}

func TestHasLabel(t *testing.T) {
	issue := Issue{Labels: []Label{{Name: "todo"}, {Name: "bug"}}}
	if !issue.hasLabel("todo") {
		t.Error("should have todo")
	}
	if issue.hasLabel("nope") {
		t.Error("should not have nope")
	}
}

func TestIssueKey(t *testing.T) {
	issue := Issue{Repo: "owner/repo", Number: 42}
	if got := issue.key(); got != "owner/repo#42" {
		t.Errorf("key = %q", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/foo"); got != home+"/foo" {
		t.Errorf("expandHome(~/foo) = %q", got)
	}
	if got := expandHome("/abs"); got != "/abs" {
		t.Errorf("expandHome(/abs) = %q", got)
	}
}

func TestTracker(t *testing.T) {
	tr := newTracker()
	if !tr.tryAcquire("repo#1") {
		t.Error("first acquire should succeed")
	}
	if tr.tryAcquire("repo#1") {
		t.Error("double acquire should fail")
	}
	tr.release("repo#1")
	if !tr.tryAcquire("repo#1") {
		t.Error("acquire after release should succeed")
	}
}

func TestCountBotErrors(t *testing.T) {
	issue := Issue{
		Comments: []Comment{
			{Body: "please fix this"},
			{Body: "claude-bot encountered an error:\n```\nsome error\n```\nNeeds manual attention."},
			{Body: "I added more context"},
			{Body: "claude-bot encountered an error:\n```\nanother error\n```\nNeeds manual attention."},
		},
	}
	if got := countBotErrors(issue); got != 2 {
		t.Errorf("countBotErrors = %d, want 2", got)
	}
}

func TestHasBotComment(t *testing.T) {
	// User mentioning "claude-bot" should NOT be detected as a bot comment
	userIssue := Issue{Comments: []Comment{{Body: "I love claude-bot!"}}}
	if hasBotComment(userIssue) {
		t.Error("user comment should not be detected as bot comment")
	}

	// Bot comment with marker SHOULD be detected
	botIssue := Issue{Comments: []Comment{{Body: "Hey thanks!\n" + botCommentMarker}}}
	if !hasBotComment(botIssue) {
		t.Error("bot comment with marker should be detected")
	}

	// No comments at all
	emptyIssue := Issue{}
	if hasBotComment(emptyIssue) {
		t.Error("empty issue should not have bot comment")
	}
}

func TestLoadDotEnvQuotes(t *testing.T) {
	// Create a temp .env file with quoted values
	dir := t.TempDir()
	envFile := dir + "/.env"
	os.WriteFile(envFile, []byte("TEST_DQ=\"double quoted\"\nTEST_SQ='single quoted'\nTEST_NQ=no quotes\n"), 0644)

	// Save cwd, change to temp dir, run loadDotEnv, restore cwd
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	// Clear test env vars
	t.Setenv("TEST_DQ", "")
	t.Setenv("TEST_SQ", "")
	t.Setenv("TEST_NQ", "")
	os.Unsetenv("TEST_DQ")
	os.Unsetenv("TEST_SQ")
	os.Unsetenv("TEST_NQ")

	loadDotEnv()

	if v := os.Getenv("TEST_DQ"); v != "double quoted" {
		t.Errorf("TEST_DQ = %q, want %q", v, "double quoted")
	}
	if v := os.Getenv("TEST_SQ"); v != "single quoted" {
		t.Errorf("TEST_SQ = %q, want %q", v, "single quoted")
	}
	if v := os.Getenv("TEST_NQ"); v != "no quotes" {
		t.Errorf("TEST_NQ = %q, want %q", v, "no quotes")
	}

	// Clean up
	os.Unsetenv("TEST_DQ")
	os.Unsetenv("TEST_SQ")
	os.Unsetenv("TEST_NQ")
}

func TestFilterEnv(t *testing.T) {
	env := []string{"PATH=/usr/bin", "CLAUDECODE=1", "HOME=/home/user", "CLAUDECODEMORE=x"}
	filtered := filterEnv(env, "CLAUDECODE")
	for _, e := range filtered {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Errorf("CLAUDECODE should be filtered, got %q", e)
		}
	}
	if len(filtered) != 3 {
		t.Errorf("expected 3 entries, got %d: %v", len(filtered), filtered)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	for _, key := range []string{"CB_REPOS", "CB_POLL_INTERVAL", "CB_WORKERS", "CB_MAX_RETRIES"} {
		t.Setenv(key, "")
	}
	cfg := loadConfig()
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.Workers != 3 {
		t.Errorf("Workers = %d", cfg.Workers)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d", cfg.MaxRetries)
	}
	if cfg.IssueLabel != "todo" {
		t.Errorf("IssueLabel = %q", cfg.IssueLabel)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("CB_REPOS", "owner/repo1, owner/repo2")
	t.Setenv("CB_POLL_INTERVAL", "1m")
	t.Setenv("CB_WORKERS", "8")
	t.Setenv("CB_MAX_RETRIES", "5")

	cfg := loadConfig()
	if len(cfg.Repos) != 2 {
		t.Fatalf("Repos = %v", cfg.Repos)
	}
	if cfg.Repos[1] != "owner/repo2" {
		t.Errorf("Repos[1] = %q (spaces should be trimmed)", cfg.Repos[1])
	}
	if cfg.PollInterval != time.Minute {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.Workers != 8 {
		t.Errorf("Workers = %d", cfg.Workers)
	}
}

func TestBuildPrompt(t *testing.T) {
	issue := Issue{
		Number: 42,
		Title:  "Fix bug",
		Body:   "It's broken",
		Comments: []Comment{
			{
				Author:    struct{ Login string `json:"login"` }{Login: "alice"},
				Body:      "Please fix",
				CreatedAt: "2025-01-01T00:00:00Z",
			},
		},
	}
	prompt := buildPrompt(issue)

	for _, want := range []string{"Issue #42", "Fix bug", "It's broken", "alice", "Please fix", "Do NOT commit"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// --- Integration Tests (require gh auth + network) ---

const testOwner = "joeblew999"

func TestIntegration(t *testing.T) {
	if os.Getenv("CB_INTEGRATION") != "1" {
		t.Skip("set CB_INTEGRATION=1 to run integration tests")
	}

	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		t.Fatal("gh not authenticated — run: gh auth login")
	}

	repo := fmt.Sprintf("claude-bot-test-%d", time.Now().Unix())
	fullRepo := testOwner + "/" + repo

	t.Logf("creating test repo: %s", fullRepo)
	cleanup := createTestRepo(t, repo)
	defer cleanup()

	time.Sleep(2 * time.Second)

	ctx := context.Background()
	cfg := Config{
		Repos:          []string{fullRepo},
		PollInterval:   5 * time.Second,
		Workers:        1,
		IssueLabel:     "todo",
		WIPLabel:       "in-progress",
		DoneLabel:      "done",
		NeedsInfoLabel: "needs-info",
		FailedLabel:    "failed",
		WorktreeDir:    t.TempDir(),
		RepoDir:        t.TempDir(),
		LogDir:         t.TempDir(),
		MaxTurns:       5,
		MaxRetries:     2,
	}

	t.Run("EnsureLabels", func(t *testing.T) {
		ensureLabels(ctx, cfg)
		for _, label := range []string{cfg.IssueLabel, cfg.WIPLabel, cfg.DoneLabel, cfg.NeedsInfoLabel, cfg.FailedLabel} {
			out, err := ghRun(t, "label", "list", "--repo", fullRepo, "--json", "name")
			if err != nil {
				t.Fatalf("listing labels: %v", err)
			}
			if !strings.Contains(out, label) {
				t.Errorf("label %q not found", label)
			}
		}
		ensureLabels(ctx, cfg) // idempotent
	})

	t.Run("FetchIssues", func(t *testing.T) {
		ghRun(t, "issue", "create", "--repo", fullRepo,
			"--title", "Test issue", "--body", "Test", "--label", "todo")

		// GitHub API needs a moment to propagate the new issue
		time.Sleep(3 * time.Second)

		issues, err := fetchIssues(ctx, fullRepo, "todo")
		if err != nil {
			t.Fatalf("fetchIssues: %v", err)
		}
		if len(issues) == 0 {
			t.Fatal("expected at least 1 issue")
		}
		if issues[0].Repo != fullRepo {
			t.Errorf("repo = %q", issues[0].Repo)
		}
	})

	t.Run("LabelOperations", func(t *testing.T) {
		issues, _ := fetchIssues(ctx, fullRepo, "todo")
		if len(issues) == 0 {
			t.Skip("no issues")
		}
		issue := issues[0]

		if err := addLabel(ctx, issue, cfg.WIPLabel); err != nil {
			t.Fatal(err)
		}
		updated := fetchIssue(t, ctx, fullRepo, issue.Number)
		if !updated.hasLabel(cfg.WIPLabel) {
			t.Error("expected wip label")
		}

		if err := removeLabel(ctx, issue, cfg.WIPLabel); err != nil {
			t.Fatal(err)
		}
		updated = fetchIssue(t, ctx, fullRepo, issue.Number)
		if updated.hasLabel(cfg.WIPLabel) {
			t.Error("wip label should be removed")
		}
	})

	t.Run("StalenessRecovery", func(t *testing.T) {
		ghRun(t, "issue", "create", "--repo", fullRepo,
			"--title", "Stale issue", "--body", "Stuck", "--label", "in-progress")

		recoverStaleIssues(ctx, cfg)

		stale, _ := fetchIssues(ctx, fullRepo, "in-progress")
		for _, iss := range stale {
			if strings.Contains(iss.Title, "Stale issue") {
				t.Error("stale issue should have been recovered")
			}
		}
	})
}

// --- Test Helpers ---

func createTestRepo(t *testing.T, name string) func() {
	t.Helper()
	cmd := exec.Command("gh", "repo", "create", testOwner+"/"+name,
		"--public", "--add-readme",
		"--description", "Temp test repo for claude-bot",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("creating test repo: %v\n%s", err, out)
	}
	return func() {
		t.Logf("deleting test repo: %s/%s", testOwner, name)
		cmd := exec.Command("gh", "repo", "delete", testOwner+"/"+name, "--yes")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("warning: couldn't delete test repo: %v\n%s", err, out)
		}
	}
}

func ghRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("gh", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func fetchIssue(t *testing.T, ctx context.Context, repo string, number int) Issue {
	t.Helper()
	out, err := run(ctx, "", "gh", "issue", "view",
		fmt.Sprintf("%d", number), "--repo", repo,
		"--json", "number,title,body,labels,url,comments",
	)
	if err != nil {
		t.Fatalf("fetching issue %d: %v", number, err)
	}
	var issue Issue
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		t.Fatalf("parsing issue: %v", err)
	}
	issue.Repo = repo
	return issue
}
