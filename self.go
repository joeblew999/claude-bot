package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	binaryName = "claude-bot"
	repoOwner  = "joeblew999"
	repoName   = "claude-bot"
)

// version is set at build time via -ldflags.
var version = "dev"

var targets = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
	{"windows", "arm64"},
}

// binaryPath returns the platform-appropriate binary name.
func binaryPath() string {
	name := binaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// gitCommit returns the short commit hash, or "unknown" if not in a git repo.
func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// ldflags returns the -ldflags string to embed version info.
func ldflags(commit string) string {
	return fmt.Sprintf("-X main.version=%s", commit)
}

// buildSelf compiles the binary from source for the current platform.
func buildSelf() {
	out := binaryPath()
	commit := gitCommit()
	log.Printf("[build] building %s (%s)...", out, commit)
	cmd := exec.Command("go", "build", "-ldflags", ldflags(commit), "-o", out, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("[build] failed: %v", err)
	}
	log.Printf("[build] done: %s", out)
}

// selfRelease cross-compiles all targets, tags, and publishes a GitHub release.
// Requires: go, gh (authenticated).
func selfRelease() {
	commit := gitCommit()
	if commit == "unknown" {
		log.Fatalf("[release] can't get git commit — not in a git repo?")
	}

	// Cross-compile all targets
	var assets []string
	for _, t := range targets {
		name := fmt.Sprintf("%s-%s-%s", binaryName, t.goos, t.goarch)
		if t.goos == "windows" {
			name += ".exe"
		}

		log.Printf("[release] building %s...", name)
		cmd := exec.Command("go", "build", "-ldflags", ldflags(commit), "-o", name, ".")
		cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, "CGO_ENABLED=0")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("[release] build failed for %s/%s: %v", t.goos, t.goarch, err)
		}
		assets = append(assets, name)
	}

	// Delete existing latest release (idempotent)
	exec.Command("gh", "release", "delete", "latest", "--repo", repoOwner+"/"+repoName, "--yes", "--cleanup-tag").Run()

	// Create release with all assets
	args := []string{"release", "create", "latest",
		"--repo", repoOwner + "/" + repoName,
		"--title", "Latest Build (" + commit + ")",
		"--notes", "Built from commit " + commit,
	}
	args = append(args, assets...)

	log.Printf("[release] publishing to GitHub...")
	cmd := exec.Command("gh", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("[release] publish failed: %v", err)
	}

	// Clean up build artifacts
	for _, a := range assets {
		os.Remove(a)
	}

	log.Printf("[release] done — %d binaries published", len(assets))
}

// selfUpdate downloads the latest release from GitHub and replaces the current binary.
// Idempotent: no-op if download fails (keeps current binary).
func selfUpdate() {
	asset := fmt.Sprintf("%s-%s-%s", binaryName, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}

	url := fmt.Sprintf("https://github.com/%s/%s/releases/download/latest/%s", repoOwner, repoName, asset)
	log.Printf("[update] downloading %s", url)

	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("[update] download failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Fatalf("[update] no release found (HTTP %d) — run --build to build from source", resp.StatusCode)
	}

	// Always write to ./claude-bot (not os.Executable which may be a go run temp)
	exe := binaryPath()

	tmp := exe + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		log.Fatalf("[update] can't create temp file: %v", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		log.Fatalf("[update] download interrupted: %v", err)
	}
	f.Close()

	if err := os.Chmod(tmp, 0755); err != nil {
		os.Remove(tmp)
		log.Fatalf("[update] chmod failed: %v", err)
	}

	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		log.Fatalf("[update] replace failed: %v", err)
	}

	log.Printf("[update] updated to latest release")
}
