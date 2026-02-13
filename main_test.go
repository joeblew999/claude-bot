package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHelloFlag(t *testing.T) {
	// Build the binary
	cmd := exec.Command("go", "build", "-o", "claude-bot-test", ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to build: %v", err)
	}
	defer os.Remove("claude-bot-test")

	// Run with --hello
	out, err := exec.Command("./claude-bot-test", "--hello").CombinedOutput()
	if err != nil {
		t.Fatalf("--hello failed: %v\n%s", err, out)
	}

	got := strings.TrimSpace(string(out))
	want := "Hello from claude-bot!"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
