package task

import (
	"os"
	"path/filepath"
	"testing"
)

// The bump must classify a submodule by the presence of its OWN .git and nothing else.
//
// Measured (charly#864): in a fresh `git worktree` every submodule directory exists and holds
// nothing. The bump used to PRE-FLIGHT those paths and refuse the whole run over them ("not an
// initialized checkout"); it now ADVANCES them in the index (opencharly/plugin-task#37), and this
// classification is what decides which of the two happens. So the test below is not about a
// skip list any more: it is about the branch the bump takes.
//
// The presence of the submodule's own .git is also the ONLY thing this reads. A git command
// inside an uninitialized directory would walk UP to the superproject and read its HEAD — the
// charly#768 hazard — so a plain file test is the whole contract.
func TestSubmoduleInitializedClassifiedByDotGit(t *testing.T) {
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
	if !submoduleInitialized(proj, "init") {
		t.Errorf("a submodule with its own .git must read as initialized")
	}
	for _, p := range []string{"empty", "absent"} {
		if submoduleInitialized(proj, p) {
			t.Errorf("%q has no .git of its own and must read as NOT initialized", p)
		}
	}
}

// A path with a checkout of its own must read as initialized, whatever else is around it.
func TestSubmoduleInitializedTrueOnInitializedTree(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "a", ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !submoduleInitialized(proj, "a") {
		t.Errorf("an initialized subtree must read as initialized")
	}
}

// resolveRelativeURL is the one place a RELATIVE submodule url is turned into something
// `git ls-remote` can read, and it is the way git itself resolves one — MEASURED
// against `git submodule init`, which writes the resolved url into .git/config. The
// superproject's url is treated as a DIRECTORY and every ".." pops one component of it,
// so "../sibling.git" is a SIBLING of the superproject and "./same.git" is INSIDE it.
func TestResolveRelativeURL(t *testing.T) {
	cases := []struct {
		base, rel, want string
	}{
		{"file:///tmp/w/host/org/super.git", "../sibling.git", "file:///tmp/w/host/org/sibling.git"},
		{"file:///tmp/w/host/org/super.git", "../../up.git", "file:///tmp/w/host/up.git"},
		{"file:///tmp/w/host/org/super.git", "./same.git", "file:///tmp/w/host/org/super.git/same.git"},
		{"file:///tmp/w/host/org/super.git", "../sub/deep.git", "file:///tmp/w/host/org/sub/deep.git"},
		{"https://github.com/opencharly/opencharly.git", "../other.git", "https://github.com/opencharly/other.git"},
		{"/tmp/fixture/umb", "../src", "/tmp/fixture/src"},
		{"", "../src", ""},
	}
	for _, c := range cases {
		if got := resolveRelativeURL(c.base, c.rel); got != c.want {
			t.Errorf("resolveRelativeURL(%q, %q) = %q; want %q", c.base, c.rel, got, c.want)
		}
	}
}
