package task

import (
	"os"
	"path/filepath"
	"testing"
)

// The pre-flight must classify a submodule by the presence of its OWN .git and nothing else.
//
// Measured (charly#864): in a fresh `git worktree` every submodule directory exists and holds
// nothing, so `bump` failed 422 times per sync with "submodule %q is not an initialized checkout".
// The condition is not a failure to advance - an uninitialized checkout has no pin to move, which
// the `status` mode already states - so it is pre-flighted and reported once.
func TestUninitializedSubmodulesClassifiedByDotGit(t *testing.T) {
	proj := t.TempDir()
	for _, p := range []string{"init", "empty"} {
		if err := os.MkdirAll(filepath.Join(proj, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// An INITIALIZED submodule carries its own .git (a file for a worktree/submodule gitdir).
	if err := os.WriteFile(filepath.Join(proj, "init", ".git"), []byte("gitdir: ../../.git/modules/init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// "empty" exists and holds nothing - the worktree shape. "absent" does not exist at all.
	paths := []string{"init", "empty", "absent"}

	got := uninitializedSubmodules(proj, paths)
	if len(got) != 2 {
		t.Fatalf("uninitializedSubmodules = %v; want the 2 without .git", got)
	}
	for _, want := range []string{"empty", "absent"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q not reported as uninitialized; got %v", want, got)
		}
	}
	for _, g := range got {
		if g == "init" {
			t.Errorf("an INITIALIZED submodule was reported uninitialized: %v", got)
		}
	}
}

// A fully initialized project must produce NO skips, so the wrapper's note cannot appear on a
// healthy tree.
func TestUninitializedSubmodulesNoneOnInitializedTree(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "a", ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := uninitializedSubmodules(proj, []string{"a"}); len(got) != 0 {
		t.Errorf("initialized tree reported %v; want none", got)
	}
}
