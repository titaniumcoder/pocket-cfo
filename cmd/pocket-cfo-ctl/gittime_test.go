package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func commitRepo(t *testing.T, files ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")

	for i, name := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", name)
		stamp := time.Date(2026, 1, 1+i, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
		cmd := exec.Command("git", "-C", dir, "commit", "-qm", name)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit %s: %v\n%s", name, err, out)
		}
	}
	return dir
}

func TestGitClockNewer(t *testing.T) {
	dir := commitRepo(t, "build/INV-0000000001-paid.pdf", "data/paid-invoices.json")
	t.Chdir(dir)

	clock, err := newGitClock()
	if err != nil {
		t.Fatal(err)
	}

	newer, err := clock.newer("build/INV-0000000001-paid.pdf", "data/paid-invoices.json")
	if err != nil {
		t.Fatal(err)
	}
	if !newer {
		t.Error("the paid list was committed after the PDF, so the PDF is stale")
	}

	notNewer, err := clock.newer("data/paid-invoices.json", "build/INV-0000000001-paid.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if notNewer {
		t.Error("the PDF is older than the paid list, so it cannot have made it stale")
	}
}

func TestGitClockTreatsADirtyFileAsNewer(t *testing.T) {
	dir := commitRepo(t, "data/paid-invoices.json", "build/INV-0000000001-paid.pdf")
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "data", "paid-invoices.json"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	clock, err := newGitClock()
	if err != nil {
		t.Fatal(err)
	}
	newer, err := clock.newer("build/INV-0000000001-paid.pdf", "data/paid-invoices.json")
	if err != nil {
		t.Fatal(err)
	}
	if !newer {
		t.Error("an uncommitted edit to the paid list must make the stamped PDF stale")
	}
}

func TestGitClockFailsLoudlyOnAShallowClone(t *testing.T) {
	origin := commitRepo(t, "data/paid-invoices.json", "build/INV-0000000001-paid.pdf")

	shallow := t.TempDir()
	clone := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+origin, shallow)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Skipf("cannot make a shallow clone here: %v\n%s", err, out)
	}
	t.Chdir(shallow)

	_, err := newGitClock()
	if err == nil {
		t.Fatal("a shallow clone must refuse to answer, not report every file as the same age")
	}
	if !strings.Contains(err.Error(), "shallow") {
		t.Errorf("error = %q, want it to name the shallow clone", err)
	}
}

func TestGitClockUsesTheMtimeOfAnUntrackedFile(t *testing.T) {
	dir := commitRepo(t, "data/paid-invoices.json")
	t.Chdir(dir)

	untracked := filepath.Join(dir, "build", "INV-0000000001-paid.pdf")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("%PDF-"), 0o644); err != nil {
		t.Fatal(err)
	}

	clock, err := newGitClock()
	if err != nil {
		t.Fatal(err)
	}
	at, err := clock.timeOf("build/INV-0000000001-paid.pdf")
	if err != nil {
		t.Fatalf("an untracked file should fall back to its mtime: %v", err)
	}
	if at.IsZero() {
		t.Error("timeOf returned the zero time for an untracked file")
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "INV-0000000001.pdf")

	if err := os.WriteFile(path, []byte("old contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("%PDF-1.4 new")); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "%PDF-1.4 new" {
		t.Errorf("contents = %q, want the new bytes", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("%d files in the build dir, want 1 — a temp file was left behind", len(entries))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("mode = %v, want 0644", perm)
	}
}
