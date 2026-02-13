package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
)

const (
	binaryName = "claude-bot"
	repoOwner  = "joeblew999"
	repoName   = "claude-bot"
)

// binaryPath returns the platform-appropriate binary name.
func binaryPath() string {
	name := binaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// buildSelf compiles the binary from source using `go build`.
func buildSelf() {
	out := binaryPath()
	log.Printf("[build] building %s...", out)
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("[build] failed: %v", err)
	}
	log.Printf("[build] done: %s", out)
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

	// Write to temp file, then replace current binary
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("[update] can't find current binary: %v", err)
	}

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

	// Make executable
	if err := os.Chmod(tmp, 0755); err != nil {
		os.Remove(tmp)
		log.Fatalf("[update] chmod failed: %v", err)
	}

	// Atomic replace
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		log.Fatalf("[update] replace failed: %v", err)
	}

	log.Printf("[update] updated to latest release")
}
