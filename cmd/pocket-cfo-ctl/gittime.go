package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type gitClock struct {
	dirty map[string]bool
}

func newGitClock() (*gitClock, error) {
	if shallow, err := isShallow(); err != nil {
		return nil, err
	} else if shallow {
		return nil, errors.New("this is a shallow clone, so per-file commit timestamps are all the same and nothing can be told to be stale — use fetch-depth: 0")
	}

	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("git status: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git status: %w", err)
	}

	dirty := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if _, after, found := strings.Cut(path, " -> "); found {
			path = after
		}
		dirty[strings.Trim(path, `"`)] = true
	}
	return &gitClock{dirty: dirty}, nil
}

func (g *gitClock) timeOf(path string) (time.Time, error) {
	if g.dirty[path] {
		return modTime(path)
	}

	out, err := exec.Command("git", "log", "-1", "--format=%ct", "--", path).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return time.Time{}, fmt.Errorf("git log %s: %s", path, strings.TrimSpace(string(ee.Stderr)))
		}
		return time.Time{}, fmt.Errorf("git log %s: %w", path, err)
	}

	stamp := strings.TrimSpace(string(out))
	if stamp == "" {
		tracked, terr := isTracked(path)
		if terr != nil {
			return time.Time{}, terr
		}
		if tracked {
			return time.Time{}, fmt.Errorf("%s is tracked but has no commit in this clone — fetch-depth is too shallow to tell what is stale", path)
		}
		return modTime(path)
	}

	secs, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("git log %s: unexpected timestamp %q", path, stamp)
	}
	return time.Unix(secs, 0), nil
}

func (g *gitClock) newer(target string, sources ...string) (bool, error) {
	targetAt, err := g.timeOf(target)
	if err != nil {
		return false, err
	}
	for _, src := range sources {
		if _, err := os.Stat(src); err != nil {
			continue
		}
		srcAt, err := g.timeOf(src)
		if err != nil {
			return false, err
		}
		if srcAt.After(targetAt) {
			return true, nil
		}
	}
	return false, nil
}

func isShallow() (bool, error) {
	out, err := exec.Command("git", "rev-parse", "--is-shallow-repository").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return false, fmt.Errorf("git rev-parse: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return false, fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func isTracked(path string) (bool, error) {
	err := exec.Command("git", "ls-files", "--error-unmatch", "--", path).Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false, nil
	}
	return false, fmt.Errorf("git ls-files %s: %w", path, err)
}

func modTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
