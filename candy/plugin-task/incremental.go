package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/opencharly/spec/spec"
)

// incremental.go — the Go-Task staleness model (sources/generates/status) and the
// platform gate.

// taskUpToDate reports whether the task is up to date and why (for the message). It
// is the OR of the two independent up-to-date signals:
//   - status: every status command exits 0 (the author's explicit probe);
//   - sources/generates: every generated path exists AND is newer than every source.
//
// An absent signal is not "up to date" (a task with no status/sources always runs).
func taskUpToDate(ctx context.Context, dir string, t spec.Task, params map[string]string) (bool, string) {
	if len(t.Status) > 0 {
		allOK := true
		for _, cmd := range t.Status {
			code, err := runHostShell(ctx, dir, t.Env, params, cmd)
			if err != nil || code != 0 {
				allOK = false
				break
			}
		}
		if allOK {
			return true, "up to date (status)"
		}
	}
	if len(t.Sources) > 0 && len(t.Generates) > 0 {
		if newer, why := generatesNewerThanSources(dir, t.Sources, t.Generates); newer {
			return true, why
		}
	}
	return false, ""
}

// generatesNewerThanSources reports whether every generated path exists and is
// strictly newer than every source path (by mtime). A missing generated path or an
// expansion that matches nothing means NOT up to date (safe: run the task).
func generatesNewerThanSources(dir string, sources, generates []string) (bool, string) {
	var newestSource int64
	for _, pat := range sources {
		matches, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil {
				if fi.ModTime().UnixNano() > newestSource {
					newestSource = fi.ModTime().UnixNano()
				}
			}
		}
	}
	generatedAny := false
	for _, pat := range generates {
		matches, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, m := range matches {
			generatedAny = true
			fi, err := os.Stat(m)
			if err != nil {
				return false, ""
			}
			if fi.ModTime().UnixNano() <= newestSource {
				return false, ""
			}
		}
	}
	if !generatedAny {
		return false, ""
	}
	return true, "up to date (sources older than generates)"
}

// platformSkip returns a non-empty reason when the task does not apply to the
// current OS (runtime.GOOS). Both platforms and exclude_platforms are honored; an
// empty platforms list means "all platforms".
func platformSkip(t spec.Task) string {
	goos := runtime.GOOS
	if len(t.Platforms) > 0 && !contains(t.Platforms, goos) {
		return fmt.Sprintf("platform %q not in %v", goos, t.Platforms)
	}
	if contains(t.ExcludePlatforms, goos) {
		return fmt.Sprintf("platform %q excluded", goos)
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
