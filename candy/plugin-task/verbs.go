package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/opencharly/plugin-task/candy/plugin-task/params"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// verbs.go — the four GENERIC, domain-neutral maintenance verbs. Each is driven by
// the REPO's own data (paths, pairs, keys) carried in its authored plugin_input,
// so the plugin stays reusable by any repository (R3 / the kernel-plugin boundary
// law) — no org name, repo name, or distro list is baked in.
//
// They are HOST-NATIVE like the task engine: a maintenance verb operates on the
// repository CHECKOUT the charly process runs in (`charly task sync`, etc.), so
// they resolve the project dir the same way loadTaskSet does (the process cwd) and
// run git/go/file operations against it. The compiled-in placement gives them the
// host checkout; that is the same placement requirement command:task has.

// invokeMaintenanceVerb decodes the op envelope's nested plugin_input and runs the
// named generic maintenance verb, returning the plan-harness verdict shape.
func invokeMaintenanceVerb(req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	var opEnv struct {
		PluginInput map[string]any `json:"plugin_input"`
	}
	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &opEnv); err != nil {
			return nil, fmt.Errorf("plugin-task: decode verb input: %w", err)
		}
	}
	status, msg := runMaintenanceVerb(req.GetReserved(), opEnv.PluginInput)
	return verbResult(status, msg)
}

// runMaintenanceVerb decodes a verb's plugin_input and dispatches to its handler,
// returning the plan-harness verdict shape (status pass/fail/skip + message). It is
// a package VARIABLE so a test can substitute a fixture project dir (the same DI
// seam verbResolverFor uses) — the live path resolves the process cwd.
var runMaintenanceVerb = func(word string, input map[string]any) (spec.Status, string) {
	projDir, err := os.Getwd()
	if err != nil {
		return spec.StatusFail, fmt.Sprintf("resolve cwd: %v", err)
	}
	return runMaintenanceVerbIn(projDir, word, input)
}

// runMaintenanceVerbIn is the testable core: it runs word against an explicit
// project dir.
func runMaintenanceVerbIn(projDir, word string, input map[string]any) (spec.Status, string) {
	switch word {
	case "git-submodules":
		var in params.GitSubmodulesInput
		if derr := decodeInput(input, &in); derr != nil {
			return spec.StatusFail, derr.Error()
		}
		return runGitSubmodules(projDir, in)
	case "file-parity":
		var in params.FileParityInput
		if derr := decodeInput(input, &in); derr != nil {
			return spec.StatusFail, derr.Error()
		}
		return runFileParity(projDir, in)
	case "splice-region":
		var in params.SpliceRegionInput
		if derr := decodeInput(input, &in); derr != nil {
			return spec.StatusFail, derr.Error()
		}
		return runSpliceRegion(projDir, in)
	case "module-pins":
		var in params.ModulePinsInput
		if derr := decodeInput(input, &in); derr != nil {
			return spec.StatusFail, derr.Error()
		}
		return runModulePins(projDir, in)
	case "prune":
		var in params.PruneInput
		if derr := decodeInput(input, &in); derr != nil {
			return spec.StatusFail, derr.Error()
		}
		return runPrune(projDir, in)
	default:
		return spec.StatusFail, fmt.Sprintf("plugin-task: unknown maintenance verb %q", word)
	}
}

// decodeInput re-marshals an opaque plugin_input map into a typed param struct,
// surfacing an unknown/mis-typed field loudly (never a silent zero value).
func decodeInput(in map[string]any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("decode verb input: %w", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode verb input: %w", err)
	}
	return nil
}

// hostCapture runs a shell script on the HOST in dir, returning stdout, stderr and
// the exit code (a non-zero exit is not an error here — the caller classifies it).
func hostCapture(ctx context.Context, dir, script string) (string, string, int) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			stderr.WriteString("\n" + err.Error())
			exit = 1
		}
	}
	return stdout.String(), stderr.String(), exit
}

// submodulePaths lists every `submodule.<name>.path` in .gitmodules (sorted).
func submodulePaths(projDir string) ([]string, error) {
	out, _, exit := hostCapture(context.Background(), projDir,
		"git config -f .gitmodules --get-regexp '^submodule\\..*\\.path$' | awk '{print $2}'")
	if exit != 0 {
		return nil, fmt.Errorf("read .gitmodules (exit %d)", exit)
	}
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func submoduleBranch(projDir, path string) string {
	out, _, _ := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git config -f .gitmodules --get 'submodule.%s.branch'", path))
	return strings.TrimSpace(out)
}

// gitlink returns the recorded gitlink SHA for path in ref's tree (“ = worktree index).
func gitlink(projDir, path string) string {
	out, _, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git ls-files -s %s | awk '{print $2}'", shellQuote(path)))
	if exit != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// submoduleGitlink reads `subpath`'s recorded gitlink (SHA) from the tree at HEAD
// of the INITIALIZED submodule at `path`, through submoduleAt's walk-up-safe form
// (charly#768). Empty on any failure (missing/uninitialized submodule, absent
// path) — the caller classifies an empty result as a loud failure, never a
// vacuous pass.
func submoduleGitlink(projDir, path, subpath string) string {
	_, gitArgs, aerr := submoduleAt(projDir, path)
	if aerr != nil {
		return ""
	}
	out, _, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git %s ls-tree HEAD %s | awk '{print $3}'", gitArgs, shellQuote(subpath)))
	if exit != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// runGitSubmodules maintains `.gitmodules` pins.
//
//	status — print PATH BRANCH PIN [DIRTY] for every submodule
//	bump   — for pin_map entries, set the gitlink to pinned_from's twin; for the
//	         rest, roll to their own default-branch HEAD; never a PR branch
//	verify — assert every pin_map entry equals pinned_from's twin (policy B), then
//	         audit every `.gitmodules` path's CHECKOUT: an initialized submodule
//	         must be clean and its HEAD must equal its recorded gitlink, read
//	         through submoduleAt's walk-up-safe form (charly#768). The recorded
//	         GITLINK is read for EVERY declared path, before the checkout is
//	         classified (an index read needs no checkout), so a path declared with
//	         no gitlink FAILS. An UNINITIALIZED path (declared, a gitlink, no
//	         checkout) is CLASSIFIED and reported in the Message, never silently
//	         skipped and never counted as a clean checkout.
//
// uninitializedSubmodules returns the submodule PATHS whose checkout is absent - the normal state of
// a freshly created `git worktree` that has not run `git submodule update --init`.
//
// It performs NO git operation inside those directories, so it cannot trigger the walk-up hazard the
// submoduleAt guard exists to prevent (charly#768); it decides purely on the presence of the
// submodule's own .git, exactly as submoduleAt does before it refuses.
func uninitializedSubmodules(projDir string, paths []string) []string {
	var uninit []string
	for _, p := range paths {
		if _, statErr := os.Stat(filepath.Join(projDir, p, ".git")); statErr != nil {
			uninit = append(uninit, p)
		}
	}
	return uninit
}

// runGitSubmodules runs the verb and states the pre-flight's finding (charly#864).
//
// An uninitialized gitlink has no pin to advance, so it is NOT one of the per-path failures the walk
// records and continues past (plugin-task#13) - it is a PRECONDITION: reported ONCE, with the command
// that repairs it, instead of failing once per path. The run still FAILS LOUD (the invariant
// TestGitSubmodules_BumpUninitializedFailsLoud pins, and the reason charly#768 exists): a sync over a
// tree it could not fully advance must never report success, and must never touch the umbrella.
// Pins that CAN advance still do, so one uninitialized sibling does not strand them.
func runGitSubmodules(projDir string, in params.GitSubmodulesInput) (spec.Status, string) {
	paths, err := submodulePaths(projDir)
	if err != nil {
		return spec.StatusFail, err.Error()
	}
	uninit := uninitializedSubmodules(projDir, paths)
	status, msg := runGitSubmodulesInner(projDir, in, uninit)
	// ONLY `bump` fails loud on it. `status` is DESIGNED to report an uninitialized gitlink as a
	// marker and succeed - it has nothing to advance and is the mode an operator uses to SEE the
	// state (TestGitSubmodules_StatusUninitializedMarker pins that) - and `verify` judges policy, not
	// checkout availability. Narrowing this to the one mode whose intent is to advance pins is what
	// keeps those two behaviours exactly as they were.
	if in.Mode == "bump" && len(uninit) > 0 {
		// Same VOCABULARY as the guard itself ("not an initialized checkout"), because that phrase is
		// what every existing reader of this failure matches on - including the regression test that
		// pins charly#768 - plus the two things it never said: HOW MANY, and which ones.
		msg += fmt.Sprintf("; %d submodule(s) not an initialized checkout (%s): run `git submodule update --init`",
			len(uninit), strings.Join(uninit, ", "))
		return spec.StatusFail, msg
	}
	return status, msg
}

func runGitSubmodulesInner(projDir string, in params.GitSubmodulesInput, uninit []string) (spec.Status, string) {
	paths, err := submodulePaths(projDir)
	if err != nil {
		return spec.StatusFail, err.Error()
	}
	skipped := map[string]bool{}
	for _, s := range in.Skip {
		skipped[s] = true
	}
	// Pre-flight the uninitialized gitlinks into `skipped` (charly#864): both phases already bypass
	// anything in this map, so no phase performs a git operation in a directory whose checkout is
	// absent - the walk-up hazard charly#768 fixed stays closed - and the condition is reported once
	// by the caller rather than once per path.
	for _, p := range uninit {
		skipped[p] = true
	}

	switch in.Mode {
	case "status":
		var b strings.Builder
		fmt.Fprintf(&b, "%-28s %-10s %s\n", "PATH", "BRANCH", "PIN")
		for _, p := range paths {
			branch := submoduleBranch(projDir, p)
			// Read through submoduleAt's walk-up-safe form (charly#768): `git -C`
			// on an uninitialized submodule reads the SUPERPROJECT's HEAD. An
			// uninitialized checkout has no pin to report.
			_, gitArgs, aerr := submoduleAt(projDir, p)
			if aerr != nil {
				fmt.Fprintf(&b, "%-28s %-10s %s\n", p, branch, "- (uninitialized)")
				continue
			}
			pin, _, _ := hostCapture(context.Background(), projDir,
				fmt.Sprintf("git %s rev-parse --short HEAD 2>/dev/null || echo '-'", gitArgs))
			dirty, _, _ := hostCapture(context.Background(), projDir,
				fmt.Sprintf("git %s status --porcelain 2>/dev/null | head -1", gitArgs))
			marker := ""
			if strings.TrimSpace(dirty) != "" {
				marker = " DIRTY"
			}
			fmt.Fprintf(&b, "%-28s %-10s %s%s\n", p, branch, strings.TrimSpace(pin), marker)
		}
		return spec.StatusPass, strings.TrimRight(b.String(), "\n")

	case "bump":
		// ORDER IS LOAD-BEARING (RDD): roll the non-pinned submodules FIRST — the
		// pinned_from repo (e.g. "charly") is itself one of them — and only THEN read
		// its gitlinks to pin the pin_map entries. The reverse order pins from the
		// pinned repo's OLD HEAD and then advances it, leaving policy B violated after
		// every sync.
		//
		// ONE FAILURE NEVER STRANDS THE REST (plugin-task#13): a per-path failure is
		// RECORDED and the walk CONTINUES, so one contended checkout — e.g. a
		// momentarily held superproject `.git/index.lock` failing that path's `git add`
		// — costs that ONE pin instead of every pin sorted after it. Every failure is
		// reported together at the end, so the run's own output names all of them.
		var done, failures []string
		// pinned_from may be an EXTERNAL checkout (`../src`) rather than one of this
		// project's OWN submodules; only a pinned_from that IS one of `paths` is rolled
		// by phase 1, so only that case can be judged "did not roll". An external
		// pinned_from is read as it stands — its gitlinks are never advanced here — so
		// the refusal below must not fire for it.
		pinnedFromIsPath := false
		for _, p := range paths {
			if p == in.PinnedFrom {
				pinnedFromIsPath = true
				break
			}
		}
		pinnedFromRolled := !pinnedFromIsPath
		// Phase 1: every submodule NOT named in pin_map rolls to its default-branch HEAD.
		for _, p := range paths {
			if skipped[p] {
				continue
			}
			if _, pinned := in.PinMap[p]; pinned {
				continue
			}
			branch := submoduleBranch(projDir, p)
			if branch == "" {
				continue
			}
			if st, msg := pinSubmodule(projDir, p, "origin/"+branch); st != spec.StatusPass {
				failures = append(failures, msg)
				continue
			}
			if p == in.PinnedFrom {
				pinnedFromRolled = true
			}
			done = append(done, p)
		}
		// Phase 2: pin each pin_map entry from the (now-current) pinned_from gitlinks.
		//
		// A pinned_from that IS one of this project's submodules and did NOT roll (its
		// roll failed, or it was skipped) is REFUSED as a source: its gitlinks are the
		// OLD ones, and pinning a pin_map entry from them is exactly the policy-B
		// violation the load-bearing order above exists to prevent. The refusal is
		// REPORTED, never silent.
		if !pinnedFromRolled {
			failures = append(failures, fmt.Sprintf(
				"refused to pin %d pin_map entr(y|ies): pinned_from %q did not roll, so its gitlinks are stale and pinning from them would violate policy B",
				len(in.PinMap), in.PinnedFrom))
		} else {
			for upath, cpath := range in.PinMap {
				if skipped[upath] {
					continue
				}
				sha := submoduleGitlink(projDir, in.PinnedFrom, cpath)
				if sha == "" {
					failures = append(failures, fmt.Sprintf("no gitlink for %q in %q", cpath, in.PinnedFrom))
					continue
				}
				if st, msg := pinSubmodule(projDir, upath, sha); st != spec.StatusPass {
					failures = append(failures, msg)
					continue
				}
				done = append(done, upath)
			}
		}
		sort.Strings(done)
		sort.Strings(failures)
		if len(failures) > 0 {
			return spec.StatusFail, fmt.Sprintf("bumped %d submodule pin(s): %s; %d FAILED: %s",
				len(done), strings.Join(done, ", "), len(failures), strings.Join(failures, "; "))
		}
		return spec.StatusPass, fmt.Sprintf("bumped %d submodule pin(s): %s", len(done), strings.Join(done, ", "))

	case "verify":
		// Half 1 — policy B: every pin_map entry must equal pinned_from's twin.
		// Every failure is COLLECTED (never a first-match return) so one run names
		// all of them; the failures are sorted for a stable message.
		var failures []string
		for upath, cpath := range in.PinMap {
			want := submoduleGitlink(projDir, in.PinnedFrom, cpath)
			got := gitlink(projDir, upath)
			// A MISSING gitlink on either side must FAIL — never compare empty to
			// empty and report a vacuous policy-B pass.
			if want == "" {
				failures = append(failures, fmt.Sprintf("no gitlink for %q in %q", cpath, in.PinnedFrom))
				continue
			}
			if got == "" {
				failures = append(failures, fmt.Sprintf("no umbrella gitlink at %q", upath))
				continue
			}
			if want != got {
				failures = append(failures, fmt.Sprintf("pin mismatch: %s (%s) != %s's %s (%s)",
					upath, got, in.PinnedFrom, cpath, want))
			}
		}
		// Half 2 — the checkout audit the doc comment promises, over EVERY
		// `.gitmodules` path: the recorded gitlink must be present in the index, and
		// where a checkout exists it must be CLEAN and its HEAD must BE that gitlink.
		// Reads go through submoduleAt's walk-up-safe `--git-dir`/`--work-tree` form
		// (charly#768), so a present-but-uninitialized directory can never resolve to
		// the enclosing superproject's HEAD.
		//
		// An UNINITIALIZED path is CLASSIFIED, never silently skipped: there is no
		// checkout to audit, so it is counted and named in the Message. Its PIN is
		// audited FIRST, for every declared path — the index read needs no checkout —
		// so a path declared in `.gitmodules` with no recorded gitlink FAILS here
		// rather than passing as "uninitialized (pin audited)". A session worktree
		// initializes no submodule at all, so calling the MISSING CHECKOUT alone a
		// FAILURE would make the gate unrunnable exactly where it is meant to run;
		// the Message — the only channel that survives the pass path — carries the
		// coverage instead.
		var uninitialized []string
		checked := 0
		for _, p := range paths {
			rec := gitlink(projDir, p)
			if rec == "" {
				failures = append(failures, fmt.Sprintf("%s: no gitlink recorded in the index", p))
				continue
			}
			_, gitArgs, aerr := submoduleAt(projDir, p)
			if aerr != nil {
				uninitialized = append(uninitialized, p)
				continue
			}
			if dirty, _, _ := hostCapture(context.Background(), projDir,
				fmt.Sprintf("git %s status --porcelain", gitArgs)); strings.TrimSpace(dirty) != "" {
				failures = append(failures, fmt.Sprintf("%s: dirty working tree", p))
				continue
			}
			head, _, _ := hostCapture(context.Background(), projDir,
				fmt.Sprintf("git %s rev-parse HEAD", gitArgs))
			if h := strings.TrimSpace(head); h != rec {
				failures = append(failures, fmt.Sprintf("%s: HEAD %s != gitlink %s", p, h, rec))
				continue
			}
			checked++
		}
		if len(failures) > 0 {
			sort.Strings(failures)
			return spec.StatusFail, fmt.Sprintf("verify: %d failure(s): %s",
				len(failures), strings.Join(failures, "; "))
		}
		msg := fmt.Sprintf("verify: %d policy pin(s) hold across %d submodule(s); %d checkout(s) clean at their gitlink",
			len(in.PinMap), len(paths), checked)
		if len(uninitialized) > 0 {
			sort.Strings(uninitialized)
			msg += fmt.Sprintf("; %d uninitialized (pin audited, no checkout): %s",
				len(uninitialized), strings.Join(uninitialized, ", "))
		}
		return spec.StatusPass, msg

	default:
		return spec.StatusFail, fmt.Sprintf("git-submodules: unknown mode %q", in.Mode)
	}
}

// submoduleAt returns the absolute path of an INITIALIZED submodule checkout at
// `path` plus the `--git-dir`/`--work-tree` argv prefix that operates on exactly
// that repo. It FAILS LOUD when `path` is not an initialized submodule.
//
// WHY the guard (charly#768): `git -C <path>` only CHANGES DIRECTORY. On a
// present-but-uninitialized submodule directory — the normal state of a freshly
// created `git worktree` that has not run `git submodule update --init` — git
// WALKS UP to the enclosing repository and resolves to the SUPERPROJECT, so a
// `git -C <empty-submodule-dir> switch --detach <ref>` silently switched the
// UMBRELLA (detaching `charly task sync`'s caller from its branch). The
// `--git-dir`/`--work-tree` form cannot walk up: a stray directory resolves to
// nothing (exit 128), never to the parent.
func submoduleAt(projDir, path string) (abs, gitArgs string, err error) {
	abs = filepath.Join(projDir, path)
	dotgit := filepath.Join(abs, ".git")
	if _, statErr := os.Stat(dotgit); statErr != nil {
		return "", "", fmt.Errorf(
			"submodule %q is not an initialized checkout (%s is absent) — run `git submodule update --init %s`",
			path, dotgit, path)
	}
	return abs, fmt.Sprintf("--git-dir=%s --work-tree=%s", shellQuote(dotgit), shellQuote(abs)), nil
}

// pinSubmodule fetches ref (falling back to an explicit SHA fetch for a shallow
// clone — the exact failure sync-gitlinks.sh documents), detaches, and stages.
// It refuses an uninitialized submodule (charly#768) and drives the checkout
// through submoduleAt's walk-up-safe `--git-dir`/`--work-tree` form.
func pinSubmodule(projDir, path, ref string) (spec.Status, string) {
	_, gitArgs, aerr := submoduleAt(projDir, path)
	if aerr != nil {
		return spec.StatusFail, aerr.Error()
	}
	fetch := fmt.Sprintf(
		"set -e; git %s fetch --quiet origin; "+
			"if ! git %s cat-file -e %s^{commit} 2>/dev/null; then git %s fetch --quiet origin %s; fi; "+
			"git %s switch --quiet --detach %s",
		gitArgs, gitArgs, shellQuote(ref), gitArgs, shellQuote(ref), gitArgs, shellQuote(ref))
	_, stderr, exit := hostCapture(context.Background(), projDir, fetch)
	if exit != 0 {
		return spec.StatusFail, fmt.Sprintf("pin %s -> %s failed: %s", path, ref, strings.TrimSpace(stderr))
	}
	// Stage the gitlink in the SUPERPROJECT (a separate repo from the submodule,
	// hence a separate git invocation, run with the project dir as its cwd).
	if _, stderr, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git add %s", shellQuote(path))); exit != 0 {
		return spec.StatusFail, fmt.Sprintf("stage %s failed: %s", path, strings.TrimSpace(stderr))
	}
	return spec.StatusPass, ""
}

// runFileParity asserts (or syncs) that paired files are byte-identical.
func runFileParity(projDir string, in params.FileParityInput) (spec.Status, string) {
	if len(in.Pairs) == 0 {
		return spec.StatusFail, "file-parity: no pairs declared"
	}
	mode := in.Mode
	if mode == "" {
		mode = "check"
	}
	var drifted []string
	for _, p := range in.Pairs {
		left := filepath.Join(projDir, p.Left)
		right := filepath.Join(projDir, p.Right)
		if mode == "sync" {
			if _, _, exit := hostCapture(context.Background(), projDir,
				fmt.Sprintf("mkdir -p %s && cp %s %s", shellQuote(filepath.Dir(right)), shellQuote(left), shellQuote(right))); exit != 0 {
				return spec.StatusFail, fmt.Sprintf("sync %s -> %s failed", p.Left, p.Right)
			}
			continue
		}
		// check: both must exist and be byte-identical.
		if _, _, exit := hostCapture(context.Background(), projDir,
			fmt.Sprintf("test -f %s && test -f %s && cmp -s %s %s",
				shellQuote(left), shellQuote(right), shellQuote(left), shellQuote(right))); exit != 0 {
			drifted = append(drifted, fmt.Sprintf("%s != %s", p.Left, p.Right))
		}
	}
	if mode == "sync" {
		return spec.StatusPass, fmt.Sprintf("synced %d pair(s)", len(in.Pairs))
	}
	if len(drifted) > 0 {
		return spec.StatusFail, fmt.Sprintf("parity drift in %d pair(s): %s", len(drifted), strings.Join(drifted, "; "))
	}
	return spec.StatusPass, fmt.Sprintf("parity: %d pair(s) identical", len(in.Pairs))
}

// runSpliceRegion splices the marked region of fragment into target.
func runSpliceRegion(projDir string, in params.SpliceRegionInput) (spec.Status, string) {
	if in.Begin == "" || in.End == "" {
		return spec.StatusFail, "splice-region: begin/end markers are required"
	}
	target := filepath.Join(projDir, in.Target)
	fragment := filepath.Join(projDir, in.Fragment)
	mode := in.Mode
	if mode == "" {
		mode = "sync"
	}
	targetText, err := os.ReadFile(target)
	if err != nil {
		return spec.StatusFail, fmt.Sprintf("read target: %v", err)
	}
	fragText, err := os.ReadFile(fragment)
	if err != nil {
		return spec.StatusFail, fmt.Sprintf("read fragment: %v", err)
	}
	region, ok := extractRegion(string(fragText), in.Begin, in.End)
	if !ok {
		return spec.StatusFail, fmt.Sprintf("fragment %s lacks a complete %q/%q region", in.Fragment, in.Begin, in.End)
	}
	merged, ok := spliceRegion(string(targetText), in.Begin, in.End, region)
	if !ok {
		return spec.StatusFail, fmt.Sprintf("target %s lacks a complete %q/%q region", in.Target, in.Begin, in.End)
	}
	if merged == string(targetText) {
		return spec.StatusPass, fmt.Sprintf("splice-region: %s already current", in.Target)
	}
	if mode == "check" {
		return spec.StatusFail, fmt.Sprintf("splice-region: %s is stale (the marked region differs)", in.Target)
	}
	if err := os.WriteFile(target, []byte(merged), 0o644); err != nil {
		return spec.StatusFail, fmt.Sprintf("write target: %v", err)
	}
	return spec.StatusPass, fmt.Sprintf("splice-region: %s updated", in.Target)
}

// markerBounds finds the line indices [bi, ei] of the region delimited by the first
// line containing begin and the next line containing end. ok=false when either
// marker is absent. It is the ONE marker-scanner both extractRegion and
// spliceRegion call (R3).
func markerBounds(text, begin, end string) (bi, ei int, ok bool) {
	bi, ei = -1, -1
	for i, l := range strings.Split(text, "\n") {
		if bi == -1 && strings.Contains(l, begin) {
			bi = i
			continue
		}
		if bi != -1 && strings.Contains(l, end) {
			ei = i
			break
		}
	}
	return bi, ei, bi != -1 && ei != -1
}

// extractRegion returns the region between the first begin-line and the next
// end-line (inclusive), from a document.
func extractRegion(text, begin, end string) (string, bool) {
	lines := strings.Split(text, "\n")
	bi, ei, ok := markerBounds(text, begin, end)
	if !ok {
		return "", false
	}
	return strings.Join(lines[bi:ei+1], "\n"), true
}

// spliceRegion replaces the marked region of text with region (which carries its
// own markers), preserving everything outside.
func spliceRegion(text, begin, end, region string) (string, bool) {
	lines := strings.Split(text, "\n")
	bi, ei, ok := markerBounds(text, begin, end)
	if !ok {
		return "", false
	}
	out := append([]string{}, lines[:bi]...)
	out = append(out, strings.Split(region, "\n")...)
	out = append(out, lines[ei+1:]...)
	return strings.Join(out, "\n"), true
}

// runModulePins adopts source_go_mod's pins for keys into every module a glob
// matches, then tidies; in check mode it only asserts.
func runModulePins(projDir string, in params.ModulePinsInput) (spec.Status, string) {
	if in.SourceGoMod == "" || in.Glob == "" || len(in.Keys) == 0 {
		return spec.StatusFail, "module-pins: source_go_mod, glob, and keys are required"
	}
	src := filepath.Join(projDir, in.SourceGoMod)
	text, err := os.ReadFile(src)
	if err != nil {
		return spec.StatusFail, fmt.Sprintf("read source go.mod: %v", err)
	}
	want := map[string]string{}
	for _, key := range in.Keys {
		ver := requireVersion(string(text), key)
		if ver == "" {
			return spec.StatusFail, fmt.Sprintf("no require pin for %s in %s", key, in.SourceGoMod)
		}
		want[key] = ver
	}
	// Resolve the glob against the project dir (module directories). A module is in
	// the lockstep set only if it requires at least ONE of the keys — a module with
	// no key require has nothing to keep in step and is skipped (the same contract
	// the former mods:tidy sweep used).
	matches, _ := filepath.Glob(filepath.Join(projDir, in.Glob))
	var modules []string
	for _, m := range matches {
		if fi, err := os.Stat(filepath.Join(m, "go.mod")); err != nil || fi.IsDir() {
			continue
		}
		gm, err := os.ReadFile(filepath.Join(m, "go.mod"))
		if err != nil {
			continue
		}
		if !requiresAny(string(gm), in.Keys) {
			continue
		}
		modules = append(modules, m)
	}
	sort.Strings(modules)
	mode := in.Mode
	if mode == "" {
		mode = "sync"
	}
	if mode == "check" {
		var stale []string
		for _, m := range modules {
			rel, _ := filepath.Rel(projDir, m)
			gm, _ := os.ReadFile(filepath.Join(m, "go.mod"))
			for k, v := range want {
				if requireVersion(string(gm), k) != v {
					stale = append(stale, fmt.Sprintf("%s: %s != %s", rel, k, v))
				}
			}
		}
		if len(stale) > 0 {
			return spec.StatusFail, fmt.Sprintf("stale pins: %s", strings.Join(stale, "; "))
		}
		return spec.StatusPass, fmt.Sprintf("all %d module(s) carry the shared pins", len(modules))
	}
	// sync: adopt then tidy, module by module.
	edited := 0
	for _, m := range modules {
		rel, _ := filepath.Rel(projDir, m)
		var editArgs []string
		for k, v := range want {
			editArgs = append(editArgs, fmt.Sprintf("-require=%s@%s", k, v))
		}
		sort.Strings(editArgs)
		script := fmt.Sprintf("cd %s && GOWORK=off GOFLAGS=-mod=mod go mod edit %s && GOWORK=off GOFLAGS=-mod=mod go mod tidy",
			shellQuote(m), strings.Join(editArgs, " "))
		if _, stderr, exit := hostCapture(context.Background(), projDir, script); exit != 0 {
			return spec.StatusFail, fmt.Sprintf("module %s: %s", rel, strings.TrimSpace(stderr))
		}
		edited++
	}
	return spec.StatusPass, fmt.Sprintf("aligned and tidied %d module(s)", edited)
}

// requiresAny reports whether a go.mod's require list names any of the keys.
func requiresAny(gomod string, keys []string) bool {
	for _, k := range keys {
		if requireVersion(gomod, k) != "" {
			return true
		}
	}
	return false
}

// requireVersion returns the version pinned for module in a go.mod's require list,
// handling BOTH the single-line (`require M vX`) and block (`\tM vX`) forms.
func requireVersion(gomod, module string) string {
	re := regexp.MustCompile(`(?m)(?:^\s*require\s+|^\s*)` + regexp.QuoteMeta(module) + `\s+(v[^\s]+)`)
	m := re.FindStringSubmatch(gomod)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// shellQuote single-quotes a path for safe interpolation into a shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
