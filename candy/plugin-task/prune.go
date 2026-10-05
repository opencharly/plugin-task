package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencharly/plugin-task/candy/plugin-task/params"
	"github.com/opencharly/spec/spec"
)

// prune.go — verb:prune: garbage-collect MERGED-UPSTREAM session worktrees and
// branches across the umbrella root AND every submodule.
//
// WHY this exists (RCA): a session's close-out ("remove YOUR worktree, delete
// the branch once merged", the git-workflow skill's B8) is prose the agent
// self-enforces. It is NOT executed when a session is auto-closed at the
// validator's block threshold, when a fix lands on a NEW branch/PR superseding an
// abandoned one, or when a session simply ends. Nothing sweeps, so worktrees and
// branches accumulate without bound. This verb is that missing control: the ONE
// mechanical reaper the close-out contract lacked.
//
// SAFETY IS THE POINT: it prunes strictly LESS rather than ever deleting
// unmerged work.
//   - A branch is prunable ONLY when its merge is PROVEN — its tip is an ancestor
//     of `base` (origin/main), OR its PR is MERGED and its tip is contained in the
//     merged PR head (the squash-merge case git ancestry alone cannot see). A
//     branch carrying commits beyond a merged head (unmerged local work) is NEVER
//     pruned.
//   - Worktrees with modified (tracked) files are skipped, not forced.
//   - The GitHub lookup is fail-safe: unavailable (no gh, no auth, offline) =>
//     prune strictly less, never more.
//   - The default merge is `base`; a CLOSED (abandoned) PR is pruned only under the
//     explicit `include_closed` opt-in.
//
// Domain-neutral (R3 / the boundary law): the project dir is the process cwd and
// the merge target is authored input — no org, repo, or distro name is baked in.

// prInfo is one PR as reported by `gh pr list --json`.
type prInfo struct {
	Number     int    `json:"number"`
	State      string `json:"state"`
	HeadRef    string `json:"headRefName"`
	HeadRefOID string `json:"headRefOid"`
}

// ghPRSource is the injectable GitHub PR-state lookup, keyed by head branch. The
// default shells out to `gh`; a test replaces it with a fixture (no network, no
// auth). A non-nil error means "PR state unavailable" and makes prune strictly
// more conservative — it never causes a deletion.
var ghPRSource = ghListPRsForDir

// worktreeEntry is one block of `git worktree list --porcelain`.
type worktreeEntry struct {
	Path     string
	Head     string
	Branch   string // short name; "" when detached
	Detached bool
	Main     bool // the first block is the repo's own checkout
}

// prunable records one worktree/branch the sweep decided to act on.
type prunable struct {
	Repo   string // relative repo path for the report ("" = project root)
	Kind   string // "worktree" | "branch"
	Target string // worktree path or branch name
	Reason string
}

// runPrune is the verb:prune handler.
func runPrune(projDir string, in params.PruneInput) (spec.Status, string) {
	mode := in.Mode
	if mode == "" {
		mode = "report"
	}
	if mode != "report" && mode != "prune" {
		return spec.StatusFail, fmt.Sprintf("prune: unknown mode %q (report|prune)", in.Mode)
	}
	base := in.Base
	if base == "" {
		base = "origin/main"
	}

	repos := pruneRepos(projDir)

	// The GitHub PR state, memoized per repo. Unavailable => prune less.
	ghByRepo := map[string]map[string][]prInfo{}
	ghOK := map[string]bool{}
	if !in.LocalOnly {
		for _, r := range repos {
			prs, err := ghPRSource(r)
			if err != nil {
				ghOK[r] = false
				continue
			}
			ghByRepo[r] = prs
			ghOK[r] = true
		}
	}

	cwd, _ := os.Getwd()
	worktreesRoot := filepath.Join(projDir, ".worktrees")

	var acted []prunable
	var plan []prunable
	var skipped []string

	// ---- Pass 1: linked session worktrees under <project>/.worktrees/ ----
	for _, r := range repos {
		wts, err := listWorktrees(r)
		if err != nil {
			continue
		}
		for _, w := range wts {
			if w.Main || !pathWithin(w.Path, worktreesRoot) {
				continue
			}
			// Never remove a worktree we are standing inside.
			if pathWithin(cwd, w.Path) {
				skipped = append(skipped, relRepo(projDir, r)+":worktree "+w.Path+" (is cwd)")
				continue
			}
			if w.Detached || w.Branch == "" {
				skipped = append(skipped, relRepo(projDir, r)+":worktree "+w.Path+" (detached HEAD — no branch to prove merged)")
				continue
			}
			trackClean, untracked := worktreeStatus(w.Path)
			if !trackClean {
				skipped = append(skipped, relRepo(projDir, r)+":worktree "+w.Path+" (modified tracked files)")
				continue
			}
			ok, why := branchMerged(r, w.Branch, w.Head, base, ghByRepo[r], ghOK[r], in)
			if !ok {
				continue
			}
			// `git worktree remove` refuses a worktree with untracked entries, so a
			// force is required to reap it — and that DISCARDS the untracked files.
			// The count is carried into the report so the discard is never silent.
			reason := why
			if untracked > 0 {
				reason = fmt.Sprintf("%s; discards %d untracked entr%s", why, untracked, plural(untracked))
			}
			p := prunable{Repo: relRepo(projDir, r), Kind: "worktree", Target: w.Path, Reason: reason}
			plan = append(plan, p)
			if mode == "prune" {
				if err := removeWorktree(r, w.Path, untracked > 0); err != nil {
					return spec.StatusFail, fmt.Sprintf("prune: remove worktree %s: %v", w.Path, err)
				}
				acted = append(acted, p)
			}
		}
		if mode == "prune" {
			gitCapture(r, "worktree", "prune")
		}
	}

	// ---- Pass 2: branches (orphans + those just freed by pass 1) ----
	// Re-list worktrees AFTER removals so the in-use set is current.
	for _, r := range repos {
		wts, err := listWorktrees(r)
		if err != nil {
			continue
		}
		inUse := map[string]bool{}
		for _, w := range wts {
			if w.Branch != "" {
				inUse[w.Branch] = true
			}
		}
		def := defaultBranch(r, base)
		for _, b := range listLocalBranches(r) {
			if b == def || b == "main" || b == "master" {
				continue
			}
			if inUse[b] {
				continue
			}
			tip, _ := gitCapture(r, "rev-parse", "-q", "--verify", "refs/heads/"+b)
			ok, why := branchMerged(r, b, tip, base, ghByRepo[r], ghOK[r], in)
			if !ok {
				continue
			}
			p := prunable{Repo: relRepo(projDir, r), Kind: "branch", Target: b, Reason: why}
			plan = append(plan, p)
			if mode == "prune" {
				if _, exit := gitCapture(r, "branch", "-D", b); exit != 0 {
					skipped = append(skipped, relRepo(projDir, r)+":branch "+b+" (delete failed)")
					continue
				}
				acted = append(acted, p)
			}
		}
	}

	out := renderPrune(mode, base, in, plan, acted, skipped)
	return spec.StatusPass, out
}

// branchMerged decides whether branch (at tip) is safe to prune, returning the
// reason. It is DELIBERATELY conservative: an unavailable/unproven merge returns
// false. Ancestry into `base` is the primary, network-free proof; the GitHub
// lookup adds the squash-merge class (tip contained in a MERGED PR head).
func branchMerged(repoDir, branch, tip, base string, prs map[string][]prInfo, ghOK bool, in params.PruneInput) (bool, string) {
	if tip == "" {
		return false, ""
	}
	if gitIsAncestor(repoDir, tip, base) {
		return true, "tip is an ancestor of " + base
	}
	if in.LocalOnly || !ghOK || prs == nil {
		return false, ""
	}
	listed := prs[branch]
	// A MERGED PR whose head contains the tip => the work landed (squash or not).
	for _, p := range listed {
		if p.State != "MERGED" || p.HeadRefOID == "" {
			continue
		}
		if tip == p.HeadRefOID || gitIsAncestor(repoDir, tip, p.HeadRefOID) {
			return true, fmt.Sprintf("PR #%d merged; tip contained in the merged head", p.Number)
		}
	}
	// A CLOSED (abandoned) PR is pruned ONLY under the explicit opt-in, and only
	// when the tip is contained in the closed PR head (so the branch never carried
	// commits beyond the abandoned effort).
	if in.IncludeClosed {
		for _, p := range listed {
			if p.State != "CLOSED" || p.HeadRefOID == "" {
				continue
			}
			if tip == p.HeadRefOID || gitIsAncestor(repoDir, tip, p.HeadRefOID) {
				return true, fmt.Sprintf("PR #%d closed; tip contained in the closed head", p.Number)
			}
		}
	}
	return false, ""
}

// pruneRepos returns the project root plus every INITIALIZED submodule checkout.
//
// WHY the submoduleAt guard (plugin-task#16, the charly#768 class): `git -C
// <path>` only CHANGES DIRECTORY. On a present-but-uninitialized submodule
// directory — the normal state of a fresh `git worktree` before `git submodule
// update --init` — git then WALKS UP to the enclosing repository and resolves to
// the SUPERPROJECT. Every git call the sweep makes through that path (worktree
// list, for-each-ref, branch -D) would therefore read and MUTATE the project
// itself: the project's own worktrees and branches surface once per
// uninitialized submodule, and in `mode=prune` Pass 2 the `branch -D` reaches
// branches that belong to the SUPERPROJECT — including OTHER sessions' (rule 9).
// submoduleAt is the walk-up-safe guard the sibling `git-submodules` verb
// already uses; an uninitialized submodule contributes NOTHING (the sweep is
// strictly smaller, never larger).
func pruneRepos(projDir string) []string {
	repos := []string{projDir}
	subs, err := submodulePaths(projDir)
	if err != nil {
		return repos
	}
	for _, s := range subs {
		if _, _, aerr := submoduleAt(projDir, s); aerr != nil {
			continue
		}
		repos = append(repos, filepath.Join(projDir, s))
	}
	sort.Strings(repos)
	return repos
}

// listWorktrees parses `git -C repoDir worktree list --porcelain`.
func listWorktrees(repoDir string) ([]worktreeEntry, error) {
	out, exit := gitCapture(repoDir, "worktree", "list", "--porcelain")
	if exit != 0 {
		return nil, fmt.Errorf("git worktree list in %s (exit %d)", repoDir, exit)
	}
	var res []worktreeEntry
	cur := &worktreeEntry{}
	flush := func() {
		if cur.Path != "" {
			res = append(res, *cur)
		}
		cur = &worktreeEntry{}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			cur.Detached = true
		}
	}
	flush()
	for i := range res {
		res[i].Main = i == 0
	}
	return res, nil
}

// worktreeStatus reports whether the worktree has NO modified tracked files, and
// how many untracked entries it has. `git worktree remove` REFUSES a worktree with
// untracked entries, so the removal is forced for those — which DISCARDS them. The
// count is surfaced in the report so that discard is never silent.
func worktreeStatus(wt string) (trackClean bool, untracked int) {
	out, _ := gitCapture(wt, "status", "--porcelain")
	trackClean = true
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "??") {
			untracked++
			continue
		}
		trackClean = false
	}
	return trackClean, untracked
}

// listLocalBranches returns every local branch short name.
func listLocalBranches(repoDir string) []string {
	out, exit := gitCapture(repoDir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if exit != 0 {
		return nil
	}
	var branches []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			branches = append(branches, l)
		}
	}
	return branches
}

// defaultBranch resolves the repo's default branch from origin/HEAD, falling back
// to the base ref's branch, then "main".
func defaultBranch(repoDir, base string) string {
	if out, exit := gitCapture(repoDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); exit == 0 && out != "" {
		return strings.TrimPrefix(out, "origin/")
	}
	if strings.HasPrefix(base, "origin/") {
		return strings.TrimPrefix(base, "origin/")
	}
	return "main"
}

// removeWorktree removes a linked worktree; force is needed only to discard
// untracked leftovers (modified tracked files are skipped by the caller).
func removeWorktree(repoDir, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	if _, exit := gitCapture(repoDir, args...); exit != 0 {
		return fmt.Errorf("git worktree remove (exit %d)", exit)
	}
	return nil
}

// gitIsAncestor reports whether a is an ancestor of (or equal to) b in repoDir.
func gitIsAncestor(repoDir, a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	_, exit := gitCapture(repoDir, "merge-base", "--is-ancestor", a, b)
	return exit == 0
}

// gitCapture runs `git <args...>` with the working dir set to dir, returning
// trimmed stdout and the exit code (a non-zero exit is not a Go error).
func gitCapture(dir string, args ...string) (string, int) {
	script := "git"
	for _, a := range args {
		script += " " + shellQuote(a)
	}
	out, _, exit := hostCapture(context.Background(), dir, script)
	return strings.TrimSpace(out), exit
}

// plural returns "y"/"ies" for a count, so a report line reads
// "1 untracked entry" / "2 untracked entries".
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// pathWithin reports whether p is dir itself or lies under it.
func pathWithin(p, dir string) bool {
	p, err1 := filepath.Abs(p)
	dir, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, dir+string(os.PathSeparator))
}

// relRepo renders a repo dir relative to the project root for the report.
func relRepo(projDir, repoDir string) string {
	if r, err := filepath.Rel(projDir, repoDir); err == nil && r != "." {
		return r
	}
	return "."
}

// renderPrune renders the report/action summary (stable, greppable).
func renderPrune(mode, base string, in params.PruneInput, plan, acted []prunable, skipped []string) string {
	var b strings.Builder
	verb := "would prune"
	items := plan
	if mode == "prune" {
		verb = "pruned"
		items = acted
	}
	wt, br := 0, 0
	for _, p := range items {
		if p.Kind == "worktree" {
			wt++
		} else {
			br++
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Repo != items[j].Repo {
			return items[i].Repo < items[j].Repo
		}
		return items[i].Target < items[j].Target
	})
	fmt.Fprintf(&b, "prune (%s, base=%s, gh=%s): %s %d worktree(s) + %d branch(es)\n",
		mode, base, ghMode(in), verb, wt, br)
	for _, p := range items {
		fmt.Fprintf(&b, "  %-6s %-28s %s — %s\n", p.Kind, p.Repo, p.Target, p.Reason)
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "  skipped %d:\n", len(skipped))
		for _, s := range skipped {
			fmt.Fprintf(&b, "    - %s\n", s)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ghMode names the PR-state source for the report.
func ghMode(in params.PruneInput) string {
	if in.LocalOnly {
		return "off (local_only)"
	}
	if in.IncludeClosed {
		return "on (+closed)"
	}
	return "on"
}

// ghListPRsForDir resolves repoDir's origin to owner/repo, lists ALL its PRs
// (capped), and keys them by head branch. Any failure (no gh, no auth, no network,
// non-GitHub origin) returns an error, which the caller treats as "unavailable".
func ghListPRsForDir(repoDir string) (map[string][]prInfo, error) {
	url, exit := gitCapture(repoDir, "config", "--get", "remote.origin.url")
	if exit != 0 || url == "" {
		return nil, fmt.Errorf("no origin remote in %s", repoDir)
	}
	slug := normalizeGitHubSlug(url)
	if slug == "" {
		return nil, fmt.Errorf("origin is not a GitHub repo: %q", url)
	}
	out, _, exit := hostCapture(context.Background(), repoDir,
		"gh pr list --repo "+shellQuote(slug)+
			" --state all --limit 500 --json number,state,headRefName,headRefOid")
	if exit != 0 {
		return nil, fmt.Errorf("gh pr list %s (exit %d)", slug, exit)
	}
	var prs []prInfo
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("decode gh output for %s: %w", slug, err)
	}
	byBranch := map[string][]prInfo{}
	for _, p := range prs {
		byBranch[p.HeadRef] = append(byBranch[p.HeadRef], p)
	}
	return byBranch, nil
}

// normalizeGitHubSlug turns a GitHub remote URL into "owner/repo", or "" when it
// is not a GitHub remote.
func normalizeGitHubSlug(url string) string {
	s := strings.TrimSpace(strings.TrimSuffix(url, ".git"))
	for _, p := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		s = strings.TrimPrefix(s, p)
	}
	if strings.Count(s, "/") != 1 || strings.HasPrefix(s, "/") {
		return ""
	}
	return s
}
