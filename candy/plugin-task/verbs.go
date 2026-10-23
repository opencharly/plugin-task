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
	"strconv"
	"strings"
	"time"

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

// submoduleURL reads the url declared for path in `.gitmodules`. Empty when the
// path declares none — the caller reports that rather than probing nothing.
func submoduleURL(projDir, path string) string {
	out, _, _ := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git config -f .gitmodules --get 'submodule.%s.url'", path))
	return strings.TrimSpace(out)
}

// resolveUpstreamRef resolves `ref` on `url` to the commit the remote ACTUALLY
// SERVES, which is the only value a currency claim may rest on: a BRANCH head
// first, then a TAG. `which` says which namespace answered ("branch" | "tag") and is
// reported, never assumed — a tag and a branch of one name are different claims.
//
// A TAG is read through `--tags` and preferred in its PEELED form (`<ref>^{}`), so an
// annotated tag yields the COMMIT it names rather than the tag object: comparing a
// gitlink against a tag object would report every annotated tag as a mismatch.
//
// Empty sha means the ref is served by neither namespace — the loud case the caller
// classifies as a divergence, never as "no information".
func resolveUpstreamRef(projDir, url, ref string) (sha, which string) {
	if ref == "" || url == "" {
		return "", ""
	}
	if sha := remoteBranchSHA(projDir, url, ref); sha != "" {
		return sha, "branch"
	}
	out, _, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git ls-remote --tags %s %s", shellQuote(url), shellQuote(ref)))
	if exit != 0 {
		return "", ""
	}
	var tag, peeled string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if strings.HasSuffix(f[1], "^{}") {
			peeled = f[0]
			continue
		}
		if f[1] == "refs/tags/"+ref {
			tag = f[0]
		}
	}
	if peeled != "" {
		return peeled, "tag"
	}
	return tag, "tag"
}

// remoteBranchSHA asks `url` for the tip of `refs/heads/<branch>` — the value a LEAF
// pin holds, read from the REMOTE and from nothing local.
//
// It is the ONE branch read in this file. `resolveUpstreamRef` (the CURRENCY question)
// calls it, and so does the bump's index-only advance (opencharly/plugin-task#37), so
// the two can never answer the same branch differently: a pin the currency report calls
// `cur` and a pin the bump wrote are the same commit by construction.
//
// Empty means the remote does not serve that branch (or could not be reached) — the
// loud case the caller reports, never "no information".
func remoteBranchSHA(projDir, url, branch string) string {
	if url == "" || branch == "" {
		return ""
	}
	out, _, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git ls-remote %s %s", shellQuote(url), shellQuote("refs/heads/"+branch)))
	if exit != 0 {
		return ""
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// resolveSubmoduleURL returns the URL a path's pin is read from: the url declared in
// `.gitmodules`, with a RELATIVE url resolved against the SUPERPROJECT's own origin —
// the same resolution git itself performs, because a relative submodule url means "a
// sibling of this repo on the same remote".
//
// Empty means the url is missing, or is relative and the superproject has no origin to
// resolve it against — the caller reports that rather than probing nothing.
func resolveSubmoduleURL(projDir, path string) string {
	u := submoduleURL(projDir, path)
	if u == "" {
		return ""
	}
	if !strings.HasPrefix(u, "./") && !strings.HasPrefix(u, "../") {
		return u
	}
	out, _, exit := hostCapture(context.Background(), projDir, "git config --get remote.origin.url")
	if exit != 0 {
		return ""
	}
	return resolveRelativeURL(strings.TrimSpace(out), u)
}

// resolveRelativeURL resolves `rel` ("../sibling.git") against `base`
// ("https://host/org/repo.git"), the way git resolves a relative submodule url.
//
// MEASURED against git itself (`git submodule init`, which writes the RESOLVED url into
// .git/config): the superproject's url is treated as a DIRECTORY, and every ".." pops
// one component of it. With base "file:///tmp/w/host/org/super.git": "../sibling.git"
// resolves to "file:///tmp/w/host/org/sibling.git" (a SIBLING of the superproject),
// "../../up.git" to "file:///tmp/w/host/up.git", and "./same.git" to
// "file:///tmp/w/host/org/super.git/same.git" (INSIDE it).
//
// Empty when `base` is empty or the walk climbs above the root.
func resolveRelativeURL(base, rel string) string {
	if base == "" || rel == "" {
		return ""
	}
	dir := base
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "", ".":
		case "..":
			i := strings.LastIndex(dir, "/")
			if i < 0 {
				return ""
			}
			dir = dir[:i]
		default:
			dir += "/" + seg
		}
	}
	return dir
}

// nestedGitmodulesGet reads `key` (e.g. `submodule.box/arch.branch`) from the `.gitmodules`
// of the submodule at `src`. That file is the SOURCE repo's OWN pin declaration, and it is
// what makes an EDGE judgeable one hop further than the source's own gitlink: the edge's
// target is not "charly" as a whole but charly's pin at the named twin, whose ref is
// declared there and nowhere else. Empty when the source has no checkout, no .gitmodules,
// or no such key — the caller reports that rather than guessing a ref.
func nestedGitmodulesGet(projDir, src, key string) string {
	abs, _, aerr := submoduleAt(projDir, src)
	if aerr != nil {
		return ""
	}
	out, _, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git config -f %s --get %s",
			shellQuote(filepath.Join(abs, ".gitmodules")), shellQuote(key)))
	if exit != 0 {
		return ""
	}
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

// gitlinkMismatchClass names WHY a submodule's checkout does not match the gitlink the
// INDEX records, and the command that repairs it. It returns (class, remedy).
//
// The DIRECTION is the whole point (opencharly/opencharly#461). `verify` reads the recorded
// pin from the SUPERPROJECT's index (gitlink → `git ls-files -s`) and compares it against the
// submodule checkout's HEAD, and those two values are maintained by DIFFERENT verbs:
// `charly task sync` (git-submodules bump) MATERIALISES each advance and STAGES it
// (`git add <path>`), while the sanctioned branch workflow — `git switch -c <b> origin/main`,
// `git reset`, `git stash pop`, an aborted commit — rewrites the index and silently UN-stages
// those advances. The tree then holds a correctly-advanced set of pins whose index is behind
// them, and before this classifier it was reported as `HEAD <h> != gitlink <g>`,
// character-identical to a checkout that had drifted off its pin: the gate was right to be
// red, but it named the wrong thing and never named the remedy (`git add`).
//
// The two states ARE the same object-level state, so no classifier can separate them by
// CAUSE, and inventing one would be the guess this file avoids elsewhere. The honest report
// is therefore the DIRECTION plus both remedies, ordered so the cause this repo actually
// produces comes first. Only the ancestry question is answerable from the objects, and
// `merge-base --is-ancestor` answers it locally with no network read — it exits 1 for "not an
// ancestor" and 128 when the object is unknown here, and the latter is reported as such
// rather than folded into either answer.
func gitlinkMismatchClass(projDir, gitArgs, path, rec, head string) (class, remedy string) {
	isAncestor := func(a, b string) (ahead, known bool) {
		_, _, exit := hostCapture(context.Background(), projDir,
			fmt.Sprintf("git %s merge-base --is-ancestor %s %s", gitArgs, shellQuote(a), shellQuote(b)))
		return exit == 0, exit == 0 || exit == 1
	}
	if ahead, known := isAncestor(rec, head); known && ahead {
		return "unstaged advance", fmt.Sprintf(
			"the checkout is AHEAD of the pin the index records — `git add %s` records the advance `charly task sync` materialised, or `git submodule update %s` discards it",
			path, path)
	}
	if behind, known := isAncestor(head, rec); known && behind {
		return "checkout behind the index", fmt.Sprintf(
			"the index records a pin this checkout does not have — `git submodule update %s`", path)
	}
	if _, known := isAncestor(rec, head); !known {
		return "unverifiable here", fmt.Sprintf(
			"the recorded pin is absent from this checkout, so the direction cannot be judged — `git submodule update %s`", path)
	}
	return "off its recorded gitlink", fmt.Sprintf(
		"the checkout and the recorded pin are unrelated — `git -C %s fetch origin && git submodule update %s`", path, path)
}

// classTally renders the per-class counts of a failing run as ` (20 unstaged advance, 1 dirty
// working tree)`, so a long failure list is legible as a distribution rather than only as N
// rows. Sorted for a stable message; empty (never "()") when there is no class to count.
func classTally(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	classes := make([]string, 0, len(counts))
	for c := range counts {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	parts := make([]string, 0, len(classes))
	for _, c := range classes {
		parts = append(parts, fmt.Sprintf("%d %s", counts[c], c))
	}
	return " (" + strings.Join(parts, ", ") + ")"
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

// submoduleInitialized reports whether `path` has an INITIALIZED checkout - the presence
// of the submodule's OWN .git and nothing else.
//
// It performs NO git operation inside that directory, so it cannot trigger the walk-up hazard the
// submoduleAt guard exists to prevent (charly#768); it decides purely on the presence of the
// submodule's own .git, exactly as submoduleAt does before it refuses.
//
// WHY this is the bump's BRANCH and not its PRECONDITION (opencharly/plugin-task#37): a path
// with no checkout is not a broken pin. Its pin lives in the SUPERPROJECT's index, and the bump
// advances it THERE (advancePinInIndex) - so a wave never has to materialize a repo to record
// where that repo is pinned. A path WITH a checkout is MATERIALIZED, and the bump moves it, so
// the checkout and the index keep agreeing.
func submoduleInitialized(projDir, path string) bool {
	_, statErr := os.Stat(filepath.Join(projDir, path, ".git"))
	return statErr == nil
}

// pinKind is what a pin's declared intent RESOLVES to. `pinGraph` classifies it for EVERY
// path at once, because the depth rule has to be a property of the GRAPH rather than a check
// at one call site — and a rule kept at a call site is a rule the next edge SHAPE walks
// around. MEASURED (validator round 2, head 42b76d4): the check read `pinned_from` only, so
// an explicit `derived-from:<path>` edge whose source was itself an edge was never refused,
// while schema/task.cue promised a graph acyclic BY CONSTRUCTION.
type pinKind int

const (
	// kindUnpinned is also the ZERO value, and that is the safe default: a path the graph has
	// never seen cannot supply an edge to inherit either, so an unknown source can never be
	// silently accepted as a leaf.
	kindUnpinned pinKind = iota // no intent and no `branch=`: unjudgeable, and reported as such
	kindLeaf                    // `upstream:<ref>`: judged against the REMOTE ref
	kindEdge                    // `derived-from:<source>`: a `pin_map` entry, or an explicit intent
)

// pinGraph is every pin's KIND plus, for an EDGE and nothing else, the path it derives from.
//
// `source` is written only for kindEdge, so "the source of a leaf" is not expressible in this
// type: a depth-3 chain is representable here ONLY as an edge whose source is itself a key of
// `source` — which is exactly the shape the depth rule refuses. The rule therefore lives in
// the DATA, and a third edge shape added later inherits it instead of having to remember it.
type pinGraph struct {
	kind   map[string]pinKind
	source map[string]string
	// shape says HOW each of those paths is an edge, so a refusal can name it.
	shape map[string]string
}

// edgeRef is one edge and the path it derives from.
type edgeRef struct{ edge, source string }

// newPinGraph classifies EVERY path ONCE. `intentOf` is the caller's resolution of a path's
// declared intent (including the implicit `upstream:<branch>` of a `branch=`-carrying
// `.gitmodules` entry), so the graph and the walk cannot disagree about what a pin means.
func newPinGraph(paths []string, intentOf func(string) string, in params.GitSubmodulesInput) pinGraph {
	g := pinGraph{
		kind:   map[string]pinKind{},
		source: map[string]string{},
		shape:  map[string]string{},
	}
	for _, p := range paths {
		it := intentOf(p)
		switch {
		case it == "":
			g.kind[p] = kindUnpinned
		case strings.HasPrefix(it, "derived-from:"):
			g.kind[p] = kindEdge
			g.source[p] = strings.TrimSpace(strings.TrimPrefix(it, "derived-from:"))
			if twin, viaPinMap := in.PinMap[p]; viaPinMap {
				g.shape[p] = fmt.Sprintf("a `pin_map` entry (%s -> %s)", p, twin)
			} else {
				g.shape[p] = fmt.Sprintf("declares intent %q", it)
			}
		default:
			g.kind[p] = kindLeaf
		}
	}
	return g
}

// kindOf reports a path's kind. An unclassified path is kindUnpinned, never a leaf.
func (g pinGraph) kindOf(p string) pinKind { return g.kind[p] }

// edgeSources returns EVERY edge and its source, in the caller's path order. Walking THIS,
// rather than reading one struct field, is the whole difference between checking the graph
// and checking a field.
func (g pinGraph) edgeSources(paths []string) []edgeRef {
	var out []edgeRef
	for _, p := range paths {
		if g.kind[p] == kindEdge {
			out = append(out, edgeRef{edge: p, source: g.source[p]})
		}
	}
	return out
}

// runGitSubmodules maintains `.gitmodules` pins.
//
//	status — print PATH BRANCH PIN [DIRTY] for every submodule
//	bump   — for pin_map entries, set the gitlink to pinned_from's twin; for the
//	         rest, roll to their own default-branch HEAD; never a PR branch
//	         A path WITH a checkout is rolled IN THAT CHECKOUT; a path with NO
//	         checkout is advanced IN THE INDEX (opencharly/plugin-task#37), so a
//	         wave never has to materialize a repo to record where it is pinned.
//	verify — assert every derived EDGE holds (each pin_map entry equals pinned_from's
//	         twin at that path), then
//	         audit every `.gitmodules` path's CHECKOUT: an initialized submodule
//	         must be clean and its HEAD must equal its recorded gitlink, read
//	         through submoduleAt's walk-up-safe form (charly#768). A mismatch is
//	         reported with its DIRECTION and the command that repairs it
//	         (gitlinkMismatchClass), because the recorded gitlink lives in this
//	         repo's INDEX while the checkout belongs to another repo and a
//	         `charly task sync` advance that was un-staged is not a broken pin
//	         (opencharly/opencharly#461). The recorded
//	         GITLINK is read for EVERY declared path, before the checkout is
//	         classified (an index read needs no checkout), so a path declared with
//	         no gitlink FAILS. An UNINITIALIZED path (declared, a gitlink, no
//	         checkout) is CLASSIFIED and reported in the Message, never silently
//	         skipped and never counted as a clean checkout.
//
// The pre-flight that once made `bump` fail loud on an uninitialized path is GONE
// (opencharly/plugin-task#37): a path with no checkout is not a broken pin, it is a
// path whose pin lives in the superproject's INDEX, and the bump advances it there.
// The invariant charly#768 protects is untouched - no git operation ever runs inside a
// directory that has no checkout of its own, so git's walk-up to the superproject
// stays impossible - and the run still FAILS LOUD for every path it could NOT
// advance, because a sync over a tree it could not fully advance must never report
// success.
func runGitSubmodules(projDir string, in params.GitSubmodulesInput) (spec.Status, string) {
	paths, err := submodulePaths(projDir)
	if err != nil {
		return spec.StatusFail, err.Error()
	}
	skipped := map[string]bool{}
	for _, s := range in.Skip {
		skipped[s] = true
	}
	// `skipped` holds only the operator's own list - the paths this run must not
	// touch - and both bump phases honour it. The uninitialized paths used to be
	// pre-flighted into it as well; they are advanced IN THE INDEX now, not skipped
	// and not fatal (opencharly/plugin-task#37).

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
		// pinned repo's OLD HEAD and then advances it, leaving the edge violated after
		// every sync.
		//
		// ONE FAILURE NEVER STRANDS THE REST (plugin-task#13): a per-path failure is
		// RECORDED and the walk CONTINUES, so one contended checkout — e.g. a
		// momentarily held superproject `.git/index.lock` failing that path's `git add`
		// — costs that ONE pin instead of every pin sorted after it. Every failure is
		// reported together at the end, so the run's own output names all of them.
		var done, pinned, inIndex, failures []string
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
		// pinnedFromState says WHY the source may not be used, when it may not. The two
		// reasons are different facts and must not be reported as one: a source that did
		// not roll has STALE gitlinks (the edge violation the load-bearing order exists
		// to prevent), and a source with no checkout has UNREADABLE gitlinks (there is
		// no tree to read them from). "external" is a source that is not one of this
		// project's submodules at all: it is read as it stands, and neither reason
		// applies.
		//
		// The CHECKOUT decides which of the two reasons applies, and it is read FIRST
		// (opencharly/plugin-task#39): a source with no checkout cannot be rolled, so
		// its gitlinks are never "stale" - they are unreadable, whatever its `branch=`
		// says and whatever the advance did. Classifying it by the roll instead left
		// the no-`branch=` shape - which leaves phase 1 before the roll - reported as
		// a stale source, blaming gitlinks that do not exist and naming no remedy.
		pinnedFromState := "external"
		if pinnedFromIsPath {
			pinnedFromState = "not-rolled"
			if !submoduleInitialized(projDir, in.PinnedFrom) {
				pinnedFromState = "unmaterialized"
			}
		}
		// Phase 1: every submodule NOT named in pin_map rolls to its default-branch HEAD.
		//
		// A MATERIALIZED path is ROLLED: its checkout is switched to the branch tip and
		// the move is staged. A path with NO checkout is advanced IN THE INDEX: the
		// branch tip is read from the REMOTE (remoteBranchSHA) and recorded with
		// `git update-index --add --cacheinfo 160000,<sha>,<path>` (advancePinInIndex)
		// — the pin IS the index entry, so a wave never has to materialize a repo to
		// record where that repo is pinned (opencharly/plugin-task#37). The checkout,
		// when one is later materialized, is brought to that pin by
		// `git submodule update --init <path>`, which is why the order after a wave is
		// sync -> init -> verify.
		for _, p := range paths {
			if skipped[p] {
				continue
			}
			if _, pinned := in.PinMap[p]; pinned {
				continue
			}
			branch := submoduleBranch(projDir, p)
			if branch == "" {
				// A path with no `branch=` declares no ref, so there is nothing to
				// advance it to. The CURRENCY report is where that is named
				// ("no intent and no `branch=`"), and it is a property of the
				// DECLARATION, not of the checkout.
				//
				// For the PINNED_FROM source this early exit used to leave the
				// diagnosis wrong: the source's state was classified by the ROLL,
				// and a source that never rolls because it declares no ref was
				// reported as a stale one. The state is classified by the CHECKOUT
				// above, so the source is already named for what it is here
				// (opencharly/plugin-task#39).
				continue
			}
			if !submoduleInitialized(projDir, p) {
				// UNMATERIALIZED: the pin moves in the index, and only there.
				url := resolveSubmoduleURL(projDir, p)
				if url == "" {
					failures = append(failures, fmt.Sprintf(
						"no url for %q in .gitmodules, so its pin cannot be read from the remote", p))
					continue
				}
				sha := remoteBranchSHA(projDir, url, branch)
				if sha == "" {
					failures = append(failures, fmt.Sprintf(
						"the remote for %q does not serve refs/heads/%s, so its pin cannot be advanced", p, branch))
					continue
				}
				if st, msg := advancePinInIndex(projDir, p, sha); st != spec.StatusPass {
					failures = append(failures, msg)
					continue
				}
				inIndex = append(inIndex, p)
				// A source whose own pin advanced here still has no TREE to read a
				// twin from - an unmaterialized source has no checkout at all - and
				// the state already says so: it is classified by the CHECKOUT
				// before phase 1 (opencharly/plugin-task#39).
				continue
			}
			if st, msg := pinSubmodule(projDir, p, "origin/"+branch); st != spec.StatusPass {
				failures = append(failures, msg)
				continue
			}
			if p == in.PinnedFrom {
				pinnedFromState = "rolled"
			}
			done = append(done, p)
		}
		// Phase 2: pin each pin_map entry from the (now-current) pinned_from gitlinks.
		//
		// A pinned_from that IS one of this project's submodules and did NOT roll (its
		// roll failed, or it was skipped) is REFUSED as a source: its gitlinks are the
		// OLD ones, and pinning a pin_map entry from them is exactly the EDGE
		// violation the load-bearing order above exists to prevent. The refusal is
		// REPORTED, never silent.
		//
		// A pinned_from with NO CHECKOUT is refused for the other reason: its gitlinks
		// are not stale, they are UNREADABLE — the twin an edge holds lives in the
		// source's tree, and a source with no checkout has no tree to read it from. The
		// remedy is named, because it is one command.
		switch pinnedFromState {
		case "not-rolled":
			failures = append(failures, fmt.Sprintf(
				"refused to pin %d pin_map entr(y|ies): pinned_from %q did not roll, so its gitlinks are stale and pinning from them would violate the edge the caller declared",
				len(in.PinMap), in.PinnedFrom))
		case "unmaterialized":
			failures = append(failures, fmt.Sprintf(
				"refused to pin %d pin_map entr(y|ies): pinned_from %q has no checkout, so its twin gitlinks cannot be read — materialize it with `git submodule update --init %s`",
				len(in.PinMap), in.PinnedFrom, in.PinnedFrom))
		default:
			for upath, cpath := range in.PinMap {
				if skipped[upath] {
					continue
				}
				sha := submoduleGitlink(projDir, in.PinnedFrom, cpath)
				if sha == "" {
					failures = append(failures, fmt.Sprintf("no gitlink for %q in %q", cpath, in.PinnedFrom))
					continue
				}
				if !submoduleInitialized(projDir, upath) {
					// UNMATERIALIZED edge: the pin moves in the index, to the twin
					// the source's CURRENT tree records.
					if st, msg := advancePinInIndex(projDir, upath, sha); st != spec.StatusPass {
						failures = append(failures, msg)
						continue
					}
					inIndex = append(inIndex, upath)
				} else if st, msg := pinSubmodule(projDir, upath, sha); st != spec.StatusPass {
					failures = append(failures, msg)
					continue
				}
				pinned = append(pinned, upath)
			}
		}
		sort.Strings(done)
		sort.Strings(pinned)
		sort.Strings(inIndex)
		sort.Strings(failures)
		// FAILURES FIRST, deliberately: the failure list is the actionable part of this message, and the
		// rolled/pinned name lists are long enough (422 names on a full umbrella) that appending the
		// diagnostic after them is how it gets truncated away before a reader sees it. A message that
		// reports what went wrong must lead with it.
		if len(failures) > 0 {
			return spec.StatusFail, fmt.Sprintf("%d pin(s) FAILED: %s; rolled %d submodule(s) to their default-branch HEAD; advanced %d pin(s) in the index; pinned %d pin_map entr(y|ies): %s",
				len(failures), strings.Join(failures, "; "), len(done), len(inIndex), len(pinned), strings.Join(pinned, ", "))
		}
		return spec.StatusPass, fmt.Sprintf("pinned %d pin_map entr(y|ies) (%s) after rolling %d submodule(s) to their default-branch HEAD and advancing %d pin(s) in the index",
			len(pinned), strings.Join(pinned, ", "), len(done), len(inIndex))

	case "verify":
		// Half 1 — the derived EDGE: every pin_map entry must equal pinned_from's twin.
		// Every failure is COLLECTED (never a first-match return) so one run names
		// all of them; the failures are sorted for a stable message.
		var failures []string
		for upath, cpath := range in.PinMap {
			want := submoduleGitlink(projDir, in.PinnedFrom, cpath)
			got := gitlink(projDir, upath)
			// A MISSING gitlink on either side must FAIL — never compare empty to
			// empty and report a vacuous EDGE pass.
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
		// A mismatch FAILS (a commit that records the old pin is wrong) but is
		// classified by DIRECTION with its remedy (gitlinkMismatchClass), because
		// "checkout ahead of the index" and "checkout behind the index" are different
		// defects with opposite repairs, and reporting both as `HEAD != gitlink`
		// turned `charly task sync`'s own un-staged advance into a phantom break
		// (opencharly/opencharly#461).
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
		classCounts := map[string]int{}
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
				class, remedy := gitlinkMismatchClass(projDir, gitArgs, p, rec, h)
				classCounts[class]++
				failures = append(failures, fmt.Sprintf("%s: %s (HEAD %s, index gitlink %s) — %s",
					p, class, h, rec, remedy))
				continue
			}
			checked++
		}
		if len(failures) > 0 {
			sort.Strings(failures)
			return spec.StatusFail, fmt.Sprintf("verify: %d failure(s)%s: %s",
				len(failures), classTally(classCounts), strings.Join(failures, "; "))
		}
		msg := fmt.Sprintf("verify: %d policy pin(s) hold across %d submodule(s); %d checkout(s) clean at their gitlink",
			len(in.PinMap), len(paths), checked)
		if len(uninitialized) > 0 {
			sort.Strings(uninitialized)
			msg += fmt.Sprintf("; %d uninitialized (pin audited, no checkout): %s",
				len(uninitialized), strings.Join(uninitialized, ", "))
		}
		return spec.StatusPass, msg

	case "currency":
		// CURRENCY — the question `verify` does not answer (opencharly/charly#936).
		// `verify` asserts CONSISTENCY: that each checkout matches the gitlink the INDEX
		// records. A tree can be perfectly consistent while every pin is behind what its
		// repo serves — MEASURED: `verify` was GREEN while five of six `distro-*` were
		// behind their remotes. This mode asks the other question and answers it from the
		// only two objects that can answer it: the recorded gitlink, and the REMOTE.
		//
		// It reads NO checkout state, which is deliberate rather than incidental: the
		// index-vs-checkout seam (opencharly/opencharly#461) cannot arise in a walk that
		// never compares those two objects, so a currency-aware workflow cannot inherit
		// that defect by construction. (Ancestry is the ONE exception, and it is reported
		// as unjudgeable when the objects are absent rather than guessed at.)
		//
		// Every pin declares an INTENT, and the intent decides what "current" means:
		//   * a LEAF (`upstream:<ref>`) is judged against the remote ref — no local state
		//     enters the answer, so a leaf cannot inherit another repo's staleness;
		//   * an EDGE (a `pin_map` entry, i.e. derived-from:pinned_from) is judged against
		//     its SOURCE, and when the source is not current the pin is
		//     `blocked-by <source> (<source state>)` — the producer leg, NAMED rather than
		//     discovered an hour later.
		// An edge may NOT point at another edge (depth <= 2). That rule is the whole
		// difference from the contract this replaces: the old one was satisfiable at both
		// ends while both ends were stale, which is precisely how it held on five stale
		// distros.
		//
		// The FAILURE set is the STRUCTURAL one, and "structural" means exactly this: a pin the
		// workflow cannot JUDGE, or two sides that DISAGREE — including a `blocked-by` that
		// names NO leg (a source that is not a declared pin, or a twin its source declares no
		// ref for), because there is then no producer to wait for. Everything judged and merely
		// not-yet-advanced PASSES and is reported, so `behind`, `not-on-the-served-line` and a
		// `blocked-by` that DOES name a leg are legs of a
		// cross-repo sequence whose next step is another repo's PR, and a gate that failed
		// on them would be permanently red — which trains every reader to ignore it, the
		// mirror problem inverted. So the Message LEADS with the number of pins that are
		// not current, and a green run can never read as "everything is fine".
		//
		// The owner's own bound on its OWN bookkeeping, opt-in (`stale_after`). It answers the
		// question the currency report cannot: a pin that is not current is CHURN (a busy
		// upstream is behind between every wave), but a pin that is not current AND that this
		// repo has not advanced for longer than the bound is NEGLECT. The bound is on the AGE
		// of this repo's last advance of that path — NOT on the pin's distance from the ref it
		// names. A gate on the DISTANCE would be permanently red on a healthy tree, which
		// trains every reader to ignore it; the AGE is what neglect looks like, and it is
		// what a healthy tree keeps small.
		var staleAfter time.Duration
		staleBoundSet := false
		if raw := strings.TrimSpace(in.StaleAfter); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return spec.StatusFail, fmt.Sprintf(
					"git-submodules: stale_after %q is not a duration this verb can parse (e.g. \"168h\")", raw)
			}
			staleAfter, staleBoundSet = d, true
		}
		type pin struct {
			path, intent, recorded, served, which, state string
		}
		type classified struct {
			pin
			leaf bool
		}
		short := func(s string) string {
			if len(s) > 10 {
				return s[:10]
			}
			return s
		}

		// ancestryClass is the ONE definition of what a recorded commit's relation to the ref
		// its intent names MEANS. TWO call sites judge it — a LEAF against its remote ref, and
		// a `pin_map` edge's TWIN against the twin's remote ref — and they must not drift
		// apart, which they did: the twin path was ONE-WAY and had no `unmerged` arm, so a
		// squash advance on a nested twin was reported as a `diverged` state the contract does
		// not name (validator round 2, non-blocking note). Same rule, one function, both
		// shapes.
		//
		// gitArgs is the repository the ancestry is read IN — the submodule's own git dir for a
		// leaf, the source checkout's for a twin. An ABSENT checkout is the CALLER's to name,
		// because only the caller knows which one is missing.
		ancestryClass := func(p, gitArgs, rec, served string) (state, fail string) {
			recQ, servedQ := shellQuote(rec), shellQuote(served)
			if _, _, exit := hostCapture(context.Background(), projDir,
				fmt.Sprintf("git %s merge-base --is-ancestor %s %s", gitArgs, recQ, servedQ)); exit == 0 {
				return "behind", ""
			}
			if _, _, exit := hostCapture(context.Background(), projDir,
				fmt.Sprintf("git %s merge-base --is-ancestor %s %s", gitArgs, servedQ, recQ)); exit == 0 {
				return "unmerged", fmt.Sprintf(
					"%s: recorded %s is AHEAD of the served ref %s — the pin names a commit the repo does not serve",
					p, short(rec), short(served))
			}
			return "not-on-the-served-line", ""
		}
		// squashWhy is the explanation BOTH paths attach to that class. It is not a defect and
		// must not be a FAILURE: a SQUASH-MERGED branch advances without descending from its
		// previous head, so a pin taken before the merge is unreachable from the served ref —
		// MEASURED on the real umbrella (`charly`, whose own merge style is squash, and
		// `layer-charly-internals`), where a first cut that called it a divergence went red on
		// a healthy tree. A hand-pin to an unrelated commit yields the SAME two SHAs, so the
		// objects cannot tell the two apart; the honest report is the class plus the one remedy
		// that is right for both — advance the pin, which is what `task sync` does.
		squashWhy := "a squash-merged branch advances without descending from its previous head, and a hand-pin looks identical; advance the pin either way"
		var rows []classified
		var structural []string
		stateOf := map[string]string{}

		// intentOf states IMPLICIT intents rather than leaving them unstated: `bump`
		// rolls every path not in pin_map to its own default-branch HEAD, so that IS the
		// intent of such a path — it was simply never written down. A path with neither an
		// intent nor a `branch=` cannot be judged at all, and says so.
		intentOf := func(p string) string {
			if _, derived := in.PinMap[p]; derived {
				return "derived-from:" + in.PinnedFrom
			}
			if s, ok := in.Intent[p]; ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
			if b := submoduleBranch(projDir, p); b != "" {
				return "upstream:" + b + " (implicit: .gitmodules branch=)"
			}
			return ""
		}
		refOf := func(intent string) string {
			ref := strings.TrimPrefix(intent, "upstream:")
			if i := strings.Index(ref, " ("); i >= 0 {
				ref = ref[:i]
			}
			return strings.TrimSpace(ref)
		}

		// durText renders a bound or an age in the OWNER's own units: whole hours as "168h",
		// so the report echoes the bound the owner wrote rather than a Go duration literal.
		durText := func(d time.Duration) string {
			if d%time.Hour == 0 {
				return fmt.Sprintf("%dh", int(d/time.Hour))
			}
			return d.String()
		}

		// staleEligible names the states that mean "THIS repo's own pin does not hold the
		// value its intent names" — the only states whose neglect is THIS repo's business.
		// An EDGE that is `blocked-by` is NOT eligible: the producer's neglect is the
		// producer's business, and failing here over another repo's PR would be exactly the
		// permanently-red gate the org's pin rule forbids.
		staleEligible := func(state string) bool {
			switch {
			case state == "behind", state == "not-on-the-served-line":
				return true
			case strings.HasPrefix(state, "differs"):
				// The recorded value differs from the served one and no checkout was
				// available to classify the difference. It is still NOT CURRENT.
				return true
			}
			return false
		}

		// lastAdvancedAt reads the AGE of this repo's OWN bookkeeping for a pin: the
		// timestamp of the last commit that touched the pin's path. This is the owner's own
		// record of when it last advanced the pin — never the pin's distance from the served
		// ref, which is churn. An ABSENT record (the path is in no commit of this repo's
		// history) is reported as "never advanced here" rather than as a fresh pin, because
		// a path with no bookkeeping has not been advanced for as long as the repo has
		// existed.
		lastAdvancedAt := func(p string) (time.Time, bool) {
			out, _, exit := hostCapture(context.Background(), projDir,
				fmt.Sprintf("git log -1 --format=%%ct -- %s", shellQuote(p)))
			if exit != 0 {
				return time.Time{}, false
			}
			ts := strings.TrimSpace(out)
			if ts == "" {
				return time.Time{}, false
			}
			secs, err := strconv.ParseInt(ts, 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			return time.Unix(secs, 0), true
		}

		// THE GRAPH, classified once for EVERY path, then the depth rule walked over EVERY
		// edge source. This replaces a check that read `pinned_from` and therefore guarded one
		// of the two documented edge shapes: with `pin_map: {d: inner}` and
		// `intent: {e: "derived-from:d"}`, `e`'s source `d` IS a `pin_map` entry (an edge) and
		// went through unrefused. Same rule, same map, both shapes.
		g := newPinGraph(paths, intentOf, in)
		for _, e := range g.edgeSources(paths) {
			if g.kindOf(e.source) != kindEdge {
				continue
			}
			structural = append(structural, fmt.Sprintf(
				"graph depth > 1: %s derives from %q, which is ITSELF an edge (%s); an edge may not point at an edge",
				e.edge, e.source, g.shape[e.source]))
		}

		// Pass 1 — the LEAVES, and the pins that cannot be judged at all. Leaves first is
		// also the producer-first order: an edge cannot be judged before its source.
		for _, p := range paths {
			rec := gitlink(projDir, p)
			intent := intentOf(p)
			r := classified{pin: pin{path: p, intent: intent, recorded: rec}, leaf: true}
			switch {
			case rec == "":
				r.state = "no-gitlink"
				structural = append(structural, fmt.Sprintf("%s: no gitlink recorded in the index", p))
			case intent == "":
				r.state = "unpinned"
				structural = append(structural, fmt.Sprintf(
					"%s: no intent and no `branch=` in .gitmodules — declare one (`upstream:<ref>`, or a branch)", p))
			case strings.HasPrefix(intent, "derived-from:"):
				r.leaf = false
				r.state = "pending-edge"
			default:
				url := submoduleURL(projDir, p)
				if url == "" {
					r.state = "no-url"
					structural = append(structural, fmt.Sprintf("%s: no url in .gitmodules", p))
					break
				}
				served, which := resolveUpstreamRef(projDir, url, refOf(intent))
				r.served, r.which = served, which
				switch {
				case served == "":
					r.state = "ref-not-served"
					structural = append(structural, fmt.Sprintf(
						"%s: neither a branch nor a tag %q is served by %s (intent %s)", p, refOf(intent), url, intent))
				case rec == served:
					r.state = "cur"
				default:
					// Ancestry is the one read that needs objects locally. Its ABSENCE is
					// reported, never folded into a verdict.
					_, gitArgs, aerr := submoduleAt(projDir, p)
					if aerr != nil {
						r.state = "differs (no checkout to judge ancestry)"
						break
					}
					state, fail := ancestryClass(p, gitArgs, rec, served)
					r.state = state
					if fail != "" {
						structural = append(structural, fail)
					}
					if state == "not-on-the-served-line" {
						r.which = squashWhy
					}
				}
			}
			stateOf[p] = r.state
			rows = append(rows, r)
		}

		// Pass 2 — the EDGES, judged only against a source that pass 1 could judge. The two
		// edge SHAPES a declared intent may take are judged differently, because their
		// sources live in different places:
		//   * a `pin_map` entry derives from ANOTHER CHECKOUT (`pinned_from`), so its value
		//     is that checkout's recorded gitlink for the twin path, and its currency is the
		//     state of the source's OWN pin at that twin — one hop further than its gitlink;
		//   * an explicit `derived-from:<path>` derives from a SOURCE PIN OF THIS REPO, so
		//     its value is that pin's recorded gitlink read HERE, and its currency is that
		//     pin's own state. Judging it against `pinned_from` would answer a question the
		//     caller did not ask.
		for i := range rows {
			if rows[i].leaf {
				continue
			}
			p := rows[i].path
			twin, viaPinMap := in.PinMap[p]
			if !viaPinMap {
				src := strings.TrimSpace(strings.TrimPrefix(rows[i].intent, "derived-from:"))
				// An edge whose source is ITSELF an edge was refused by the depth rule above, and
				// it has no state to derive, because there is nothing to inherit. Naming that is
				// what keeps pass 1's internal `pending-edge` placeholder out of the report, and
				// it keeps the refusal independent of the ORDER `paths` happens to be in.
				if g.kindOf(src) == kindEdge {
					rows[i].state = fmt.Sprintf("blocked-by %s (itself an edge)", src)
					continue
				}
				want := gitlink(projDir, src)
				rows[i].served = want
				switch {
				case src == "":
					rows[i].state = "no-source-pin"
					structural = append(structural, fmt.Sprintf(
						"%s: an explicit derived-from intent names no source path", p))
				case want == "":
					rows[i].state = "no-source-pin"
					structural = append(structural, fmt.Sprintf(
						"%s: derived-from source %q records no gitlink in this repo's index", p, src))
				case want != rows[i].recorded:
					rows[i].state = "pin-mismatch"
					structural = append(structural, fmt.Sprintf(
						"pin-mismatch: %s records %s but its declared source %s records %s",
						p, short(rows[i].recorded), src, short(want)))
				default:
					if srcState, judged := stateOf[src]; !judged {
						rows[i].state = "blocked-by " + src + " (not a declared pin)"
						structural = append(structural, fmt.Sprintf(
							"%s: derived-from source %q is not one of this repo's declared pins, so it has no state to inherit", p, src))
					} else if srcState == "cur" {
						rows[i].state = "cur"
					} else {
						rows[i].state = "blocked-by " + src + " (" + srcState + ")"
					}
				}
				continue
			}
			src := in.PinnedFrom
			want := submoduleGitlink(projDir, src, twin)
			rows[i].served = want
			switch {
			case want == "":
				rows[i].state = "no-source-pin"
				structural = append(structural, fmt.Sprintf(
					"%s: no gitlink for %q in %q — the edge names a source that does not record it", p, twin, src))
			case want != rows[i].recorded:
				rows[i].state = "pin-mismatch"
				structural = append(structural, fmt.Sprintf(
					"pin-mismatch: %s records %s but its source %s:%s records %s",
					p, short(rows[i].recorded), src, twin, short(want)))
			default:
				// The edge HOLDS — the recorded value IS the source's value. That is
				// CONSISTENCY, and on its own it is exactly the mirror the old contract
				// settled for. The currency question moves ONE HOP FURTHER: is the TARGET PIN
				// (the source's own pin at the twin path) current? It is judged from the
				// source repo's `.gitmodules` — that is where the target's ref is declared —
				// and against the target's remote. The source being current says nothing
				// about its nested pin, which is how five stale distros stayed invisible.
				twinBranch := nestedGitmodulesGet(projDir, src, "submodule."+twin+".branch")
				twinURL := nestedGitmodulesGet(projDir, src, "submodule."+twin+".url")
				if twinBranch == "" || twinURL == "" {
					rows[i].state = "blocked-by " + src + ":" + twin + " (no ref declared)"
					rows[i].which = "the source declares no branch=/url= for " + twin + " — declare one there, or give this pin its own upstream intent"
					structural = append(structural, fmt.Sprintf(
						"%s: source %s declares no branch=/url= for %q, so the target pin cannot be judged", p, src, twin))
					break
				}
				tServed, tWhich := resolveUpstreamRef(projDir, twinURL, twinBranch)
				rows[i].served, rows[i].which = tServed, src+":"+twin+" "+tWhich
				// A twin's unserved ref stays a LEG rather than a failure, and the asymmetry
				// with the leaf arm is deliberate: the ref that is not served was declared by
				// the PRODUCER (the source repo's own `.gitmodules`), so this pin reports a
				// producer to fix — `blocked-by <src>:<twin> (ref-not-served)` — while a LEAF's
				// unserved ref is THIS repo's own wrong intent, which is structural.
				var twinState, tFail string
				switch {
				case tServed == "":
					twinState = "ref-not-served"
				case want == tServed:
					twinState = "cur"
				default:
					// The target's objects live in ITS OWN checkout, one level below the
					// source (a repo does not hold its submodules' objects). Its absence is
					// reported, never folded into a verdict.
					nested := filepath.Join(projDir, src)
					_, nArgs, nErr := submoduleAt(nested, twin)
					switch {
					case nErr != nil:
						twinState = "differs (no checkout of " + twin + " to judge ancestry)"
					default:
						// The SAME judgement the leaf path makes, so the two cannot drift.
						twinState, tFail = ancestryClass(p, nArgs, want, tServed)
						if tFail != "" {
							structural = append(structural, tFail)
						}
						if twinState == "not-on-the-served-line" {
							rows[i].which = squashWhy
						}
					}
				}
				if twinState == "cur" {
					rows[i].state = "cur"
				} else {
					rows[i].state = "blocked-by " + src + ":" + twin + " (" + twinState + ")"
				}
			}
			// NO `stateOf` write here (validator round 3). A pin's state is RECORDED in pass 1
			// and READ by exactly one consumer — an explicit `derived-from:<path>` edge
			// inheriting its source's state — and an edge whose source is itself an edge is
			// REFUSED above, before that read. So nothing can inherit a state that only this
			// branch could have written: the write was live until the depth guard landed, and the
			// guard is what retired it. Recording it anyway is the write-only state R2 deletes.
		}

		// The NEGLECT judgement (opt-in). A pin that is not current AND that this repo has
		// not advanced for longer than the owner's bound is STALE, and it FAILS. The bound
		// is on this repo's OWN bookkeeping age, so a healthy tree stays green while a
		// neglected one goes red — the two regimes the bound is calibrated against.
		var stale []string
		if staleBoundSet {
			now := time.Now()
			for _, r := range rows {
				if !r.leaf || !staleEligible(r.state) {
					continue
				}
				at, known := lastAdvancedAt(r.path)
				if !known {
					stale = append(stale, fmt.Sprintf(
						"%s: %s, and this repo's own history records no advance of it — it has never been advanced here",
						r.path, r.state))
					continue
				}
				age := now.Sub(at)
				if age > staleAfter {
					stale = append(stale, fmt.Sprintf(
						"%s: %s, and this repo has not advanced it for %s (bound %s) — advance it: mode=bump advances every pin to the ref its intent names",
						r.path, r.state, durText(age.Round(time.Hour)), durText(staleAfter)))
				}
			}
		}

		// Render: the count of pins that are NOT current leads, because a green run must
		// never be readable as "everything is fine".
		byState := map[string]int{}
		for _, r := range rows {
			byState[r.state]++
		}
		states := make([]string, 0, len(byState))
		for s := range byState {
			states = append(states, s)
		}
		sort.Strings(states)
		var tally []string
		for _, s := range states {
			tally = append(tally, fmt.Sprintf("%d %s", byState[s], s))
		}
		notCur := len(rows) - byState["cur"]

		// Intent KINDS are counted so no intent stays silent even when every pin is current,
		// and every pin that is NOT current is named with its intent, both SHAs and the
		// namespace that answered — a state without its intent is not actionable.
		leaves, implicit, derived := 0, 0, 0
		for _, r := range rows {
			switch {
			case strings.HasPrefix(r.intent, "derived-from:"):
				derived++
			default:
				leaves++
				if strings.Contains(r.intent, "implicit:") {
					implicit++
				}
			}
		}
		var detail []string
		for _, r := range rows {
			if r.state == "cur" {
				continue
			}
			d := fmt.Sprintf("%s: %s; intent %s; recorded %s", r.path, r.state, r.intent, short(r.recorded))
			if r.served != "" {
				d += "; served " + short(r.served)
			}
			if r.which != "" {
				d += " (" + r.which + ")"
			}
			detail = append(detail, d)
		}

		// Producer-first leg order: the leaves that can advance now, then the edges those
		// leaves unblock.
		var legs []string
		for _, r := range rows {
			if r.leaf && r.state == "behind" {
				legs = append(legs, fmt.Sprintf("%s (%s -> %s)", r.path, short(r.recorded), short(r.served)))
			}
		}
		for _, r := range rows {
			if !r.leaf && strings.HasPrefix(r.state, "blocked-by") {
				legs = append(legs, fmt.Sprintf("%s (%s)", r.path, r.state))
			}
		}
		legText := "none — every pin is at the ref its intent names"
		if notCur > 0 {
			legText = strings.Join(legs, " -> ")
			if legText == "" {
				legText = "none reachable from this repo"
			}
		}
		head := fmt.Sprintf("currency: %d of %d pin(s) NOT current [%s]; intents: %d upstream leaf/leaves (%d implicit), %d derived-from edge(s)",
			notCur, len(rows), strings.Join(tally, ", "), leaves, implicit, derived)
		if len(detail) > 0 {
			head += "; NOT CURRENT: " + strings.Join(detail, "; ")
		}
		if len(stale) > 0 {
			head += fmt.Sprintf("; %d STALE pin(s) (neglected for more than %s): %s",
				len(stale), durText(staleAfter), strings.Join(stale, "; "))
		}
		if len(structural) > 0 {
			sort.Strings(structural)
			return spec.StatusFail, fmt.Sprintf("%s; %d STRUCTURAL failure(s): %s; producer-first legs: %s",
				head, len(structural), strings.Join(structural, "; "), legText)
		}
		if len(stale) > 0 {
			return spec.StatusFail, fmt.Sprintf("%s; FAILED on the STALE pin(s) above; producer-first legs: %s",
				head, legText)
		}
		return spec.StatusPass, fmt.Sprintf("%s; producer-first legs: %s", head, legText)

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
	// RECONCILE THE SUBMODULE'S OWN CHECKOUTS (plugin-task#11). Advancing this
	// submodule's gitlink moves the PIN; it does not move the submodule's OWN
	// initialized nested checkouts, which stay at the gitlinks the OLD commit
	// recorded — so the shared tree goes dirty (` M <nested>`) and
	// `git-submodules verify` then fails twice over, on a change this very bump just
	// made. The reconciliation is recursive (a nested checkout can itself nest) and a
	// failure to complete it is a hard error: a bump that leaves the tree dirty is
	// worse than a bump that refuses.
	if st, msg := reconcileNestedCheckouts(projDir, path); st != spec.StatusPass {
		return st, msg
	}
	return spec.StatusPass, ""
}

// advancePinInIndex records `path`'s pin as `sha` in the SUPERPROJECT's index.
//
// THE PIN IS THE INDEX ENTRY. `verify` reads a path's pin from the index
// (`git ls-files -s`), the currency report reads it from the index, and the umbrella's
// whole pin contract is written about that entry — so advancing a pin does not NEED a
// checkout (opencharly/plugin-task#37). `git update-index --add --cacheinfo
// 160000,<sha>,<path>` writes that entry directly, and the checkout, when one is
// materialized later, is brought to it by `git submodule update --init <path>`.
//
// `--add` is required: a path can be declared in `.gitmodules` with no index entry
// yet, and a pin that could not be recorded is a pin that was never advanced. Mode
// 160000 is the gitlink mode — the same one `git submodule add` writes.
//
// No git operation runs inside `path` itself, so the walk-up hazard charly#768 closed
// stays closed: this is the superproject's index, read and written from the
// superproject's own directory.
func advancePinInIndex(projDir, path, sha string) (spec.Status, string) {
	if sha == "" {
		return spec.StatusFail, fmt.Sprintf("no sha to record for %q", path)
	}
	_, stderr, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git update-index --add --cacheinfo %s", shellQuote("160000,"+sha+","+path)))
	if exit != 0 {
		return spec.StatusFail, fmt.Sprintf("record the pin for %s in the index failed: %s",
			path, strings.TrimSpace(stderr))
	}
	return spec.StatusPass, ""
}

// reconcileNestedCheckouts brings a just-pinned submodule's OWN nested checkouts back
// onto the gitlinks its new commit records — the state `verify` demands.
//
// GUARDED twice over, and both guards are load-bearing:
//   - a submodule with no `.gitmodules` is UNTOUCHED — a plain file test, no git call, no
//     network, no new failure mode for the majority of submodules;
//   - the update is `--recursive` WITHOUT `--init`: it moves the nested checkouts that are
//     ALREADY INITIALIZED — exactly what the issue reports going stale — and never
//     initializes a nested submodule the operator did not ask for. That matters beyond
//     tidiness: `--init` would fetch nested remotes on every bump (including file-protocol
//     fixtures, which would need a protocol loosening this verb must not bake in), and a
//     bump is not the place to grow the checkout. The recursion is still there because a
//     nested checkout can itself nest.
//
// A reconcile that cannot complete is a hard error, not a warning: a bump that leaves the
// tree dirty is worse than a bump that refuses (this is the defect itself).
func reconcileNestedCheckouts(projDir, path string) (spec.Status, string) {
	abs, _, aerr := submoduleAt(projDir, path)
	if aerr != nil {
		return spec.StatusFail, aerr.Error()
	}
	if _, _, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("test -f %s", shellQuote(filepath.Join(abs, ".gitmodules")))); exit != 0 {
		return spec.StatusPass, ""
	}
	if _, stderr, exit := hostCapture(context.Background(), projDir,
		fmt.Sprintf("git -C %s submodule update --recursive", shellQuote(abs))); exit != 0 {
		return spec.StatusFail, fmt.Sprintf(
			"reconcile %s: nested checkout update failed: %s (the gitlink moved but its initialized nested checkouts did not, which leaves the tree dirty and `verify` failing)",
			path, strings.TrimSpace(stderr))
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
