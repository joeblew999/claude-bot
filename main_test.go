package main

import (
	"testing"
)

func TestDefaultGreetingNotEmpty(t *testing.T) {
	if defaultGreeting == "" {
		t.Fatal("defaultGreeting should not be empty")
	}
}

func TestGreetingSignatureNotEmpty(t *testing.T) {
	if greetingSignature == "" {
		t.Fatal("greetingSignature should not be empty")
	}
}

func TestLoadConfigDefaultGreeting(t *testing.T) {
	t.Setenv("CB_GREETING", "")

	cfg := loadConfig()
	if cfg.Greeting != defaultGreeting {
		t.Errorf("expected default greeting %q, got %q", defaultGreeting, cfg.Greeting)
	}
}

func TestLoadConfigCustomGreeting(t *testing.T) {
	custom := "Hello friend! Working on your issue now."
	t.Setenv("CB_GREETING", custom)

	cfg := loadConfig()
	if cfg.Greeting != custom {
		t.Errorf("expected custom greeting %q, got %q", custom, cfg.Greeting)
	}
}

func TestIssueHasLabel(t *testing.T) {
	issue := Issue{
		Labels: []Label{{Name: "todo"}, {Name: "bug"}},
	}
	if !issue.hasLabel("todo") {
		t.Error("expected hasLabel(todo) to be true")
	}
	if issue.hasLabel("nonexistent") {
		t.Error("expected hasLabel(nonexistent) to be false")
	}
}

func TestIssueKey(t *testing.T) {
	issue := Issue{Repo: "owner/repo", Number: 42}
	if got := issue.key(); got != "owner/repo#42" {
		t.Errorf("expected key owner/repo#42, got %s", got)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello-world"},
		{"fix: resolve bug #123", "fix-resolve-bug-123"},
		{"  spaces  ", "spaces"},
		{"UPPERCASE", "uppercase"},
		{"", ""},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBranchName(t *testing.T) {
	issue := Issue{Number: 6, Title: "Add friendly messages"}
	got := branchName(issue)
	if got != "issue-6-add-friendly-messages" {
		t.Errorf("branchName = %q, want %q", got, "issue-6-add-friendly-messages")
	}
}

func TestBranchNameEmptyTitle(t *testing.T) {
	issue := Issue{Number: 1, Title: ""}
	got := branchName(issue)
	if got != "issue-1-fix" {
		t.Errorf("branchName = %q, want %q", got, "issue-1-fix")
	}
}

func TestBuildPrompt(t *testing.T) {
	issue := Issue{
		Number: 6,
		Title:  "Add friendly messages",
		Body:   "I want the bot to greet users.",
	}
	prompt := buildPrompt(issue)
	if prompt == "" {
		t.Fatal("buildPrompt returned empty string")
	}
	// Should contain the issue number and title
	if !containsStr(prompt, "#6") {
		t.Error("prompt should contain issue number")
	}
	if !containsStr(prompt, "Add friendly messages") {
		t.Error("prompt should contain issue title")
	}
}

func TestBuildPromptWithComments(t *testing.T) {
	issue := Issue{
		Number: 6,
		Title:  "Add friendly messages",
		Body:   "I want the bot to greet users.",
		Comments: []Comment{
			{Body: "Any update?", CreatedAt: "2025-01-01T00:00:00Z"},
		},
	}
	issue.Comments[0].Author.Login = "someuser"
	prompt := buildPrompt(issue)
	if !containsStr(prompt, "someuser") {
		t.Error("prompt should contain comment author")
	}
	if !containsStr(prompt, "Any update?") {
		t.Error("prompt should contain comment body")
	}
}

func TestFilterEnv(t *testing.T) {
	env := []string{"FOO=bar", "CLAUDECODE=1", "BAZ=qux"}
	filtered := filterEnv(env, "CLAUDECODE")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(filtered))
	}
	for _, e := range filtered {
		if e == "CLAUDECODE=1" {
			t.Error("CLAUDECODE should have been filtered out")
		}
	}
}

func TestTrackerAcquireRelease(t *testing.T) {
	tr := newTracker()

	if !tr.tryAcquire("key1") {
		t.Error("first acquire should succeed")
	}
	if tr.tryAcquire("key1") {
		t.Error("second acquire of same key should fail")
	}
	if !tr.tryAcquire("key2") {
		t.Error("acquire of different key should succeed")
	}

	tr.release("key1")
	if !tr.tryAcquire("key1") {
		t.Error("acquire after release should succeed")
	}
}

func TestIssueAuthorLogin(t *testing.T) {
	issue := Issue{}
	issue.Author.Login = "testuser"
	if issue.Author.Login != "testuser" {
		t.Errorf("Author.Login = %q, want %q", issue.Author.Login, "testuser")
	}
}

func TestExpandHome(t *testing.T) {
	got := expandHome("/absolute/path")
	if got != "/absolute/path" {
		t.Errorf("expandHome(/absolute/path) = %q, want /absolute/path", got)
	}

	got = expandHome("~/test")
	if containsStr(got, "~") {
		t.Errorf("expandHome(~/test) should not contain ~, got %q", got)
	}
}

// containsStr is a test helper to check substring presence.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
