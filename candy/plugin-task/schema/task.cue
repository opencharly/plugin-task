// schema/task.cue — plugin-task's CUE schema: the authored plugin_input defs
// (command-level and verb-level) AND the `kind: task` ENTITY body this plugin owns.
//
// THE KIND BODY LIVES HERE, not in the contract module: a `kind` body belongs with the
// plugin that serves the kind. The `kind` capability declares the gate def (`plugin.go`:
// `{Class: "kind", Word: "task", Validates: true, InputDef: "#TaskValue"}`), so the host
// resolves `#TaskValue` from that DECLARATION and looks it up in the schema composed from
// the base and every served plugin unit — no kernel change and no def-name knowledge in
// the host. opencharly/spec#304 unit 2 moved it here; `spec/schema/task.cue` keeps only
// `#TaskParamSpec`, which plugin-pipeline's `args:` shares and which therefore cannot
// move.
//
// THE IMPORT RESOLVES OFFLINE. `#Task`'s `plan:` is the SAME `#Step`/`#Op` grammar a
// candy plan uses, so this schema imports the contract module the way plugin-pipeline's
// does: spec/schemacompose serves `github.com/opencharly/spec/schema` from the spec Go
// module the binary already links — no network, no module cache, no `cue.mod`, no
// published CUE module — on the serve side AND on the host side, which composes the SAME
// body under the DERIVED module declaration.

import "github.com/opencharly/spec/schema"

// #TaskInput is the typed input for a `task:` verb step composed into
// a candy/box/task plan (`- run: ... \n  task: {task: build}`), so a plan can
// invoke another declared task.
#TaskInput: {
	// task is the declared task entity name to run.
	task: string & !=""
	// params are NAME=VALUE overrides for the task's declared params.
	param?: [...string]
}

// ---------------------------------------------------------------------------
// The four GENERIC, domain-neutral maintenance verbs. Each is parameterized by
// the REPO's own data (paths, pairs, keys) carried in its authored input, so the
// plugin stays reusable by any repository (R3/boundary law) — no org name, no
// repo name, no distro list is baked in.
// ---------------------------------------------------------------------------

// #GitSubmodulesInput drives `.gitmodules` pin maintenance.
#GitSubmodulesInput: {
	// mode: status lists; bump stages new gitlinks; verify asserts CONSISTENCY —
	// that every checkout matches the gitlink the index records. currency asks the
	// OTHER question, which needs no checkout at all: whether each recorded pin is
	// the value its owning repo actually serves (a green verify is not a currency
	// claim; it passes on a tree whose submodules are all behind their remotes).
	mode: or(_submodulesModeWord)
	// pinned_from is a checkout path whose gitlinks are authoritative for the
	// entries named in pin_map (e.g. "charly"). Empty => no pinned set.
	pinned_from?: string @go(PinnedFrom)
	// pin_map maps a submodule PATH in this repo to its twin PATH in pinned_from.
	// On bump, every submodule NOT in pin_map rolls to its own default-branch HEAD.
	pin_map?: {[string]: string} @go(PinMap)
	// intent declares WHY each pin holds the value it does, so CURRENCY is
	// answerable rather than assumed. Two shapes, and they are not interchangeable:
	//
	//	"upstream:<ref>"       a LEAF — the value is the owning repo's OWN ref, read
	//	                       from the remote; no local state enters the answer.
	//	"derived-from:<path>"  an EDGE — the value is <path>'s recorded gitlink.
	//
	// An entry in pin_map IS an edge (derived-from:pinned_from) and needs no intent
	// entry. An edge may NOT point at another edge (depth <= 2): the graph is
	// acyclic BY CONSTRUCTION, not by a validation that can be skipped. A path whose
	// intent is absent AND which carries no `branch=` in .gitmodules is "unpinned" —
	// reported as such, never silently treated as a leaf.
	intent?: {[string]: string} @go(Intent)
	// skip lists submodule paths the verb must not touch.
	skip?: [...string]
	// stale_after is the OWNER's bound on its OWN bookkeeping, and it is opt-in: unset
	// => the knob is off and the currency verdict is the structural one alone.
	//
	// It is a duration ("168h"). A pin that is not current AND that this repo has not
	// advanced for LONGER than this bound is STALE, and a stale pin FAILS.
	//
	// The bound is on the AGE of this repo's last advance of that path — NOT on the
	// pin's distance from the ref it names. The distance is CHURN: a busy upstream is
	// behind between every wave, so a gate that failed on the distance would be
	// permanently red on a healthy tree, and a permanently red gate trains every reader
	// to ignore it. The age is NEGLECT, and neglect is the defect this bounds.
	stale_after?: string @go(StaleAfter)
}

// #FileParityInput asserts (or syncs) that paired files are byte-identical.
#FileParityInput: {
	// mode: check fails on any drift; sync copies left -> right.
	mode?: or(_checkSyncWord)
	// pairs are the compared file pairs, left = source of truth.
	pairs: [...#FileParityPair]
}

#FileParityPair: {
	left:  string
	right: string
}

// #SpliceRegionInput splices a marked region from a fragment into a target file.
#SpliceRegionInput: {
	// mode: sync writes; check exits non-zero on a stale target.
	mode?: or(_checkSyncWord)
	// target is the file whose marked region is replaced.
	target: string
	// fragment is the file carrying the source region (must carry the markers).
	fragment: string
	// begin / end are the region marker strings (a line containing begin starts the
	// region; a line containing end closes it), matched with the fragment's own copy.
	begin: string
	end:   string
}

// #PinClosureInput resolves the TRANSITIVE CLOSURE of a pin advance: compile the
// composed set, collect EVERY failing module, classify each BUMP (its newest
// published version already carries the fix) or EDIT (it does not), and iterate to
// a fixed point. Domain-neutral: the caller names the anchor module, any modules it
// has already composed, and the pin prefix whose versions matter.
#PinClosureInput: {
	// anchor is the module dir whose go.mod names the pins (default: <project>/charly).
	anchor?: string
	// compose lists module dirs already adopted into the probe (they win over the pins).
	compose?: [...string] @go(Compose)
	// module_prefix limits the reported pins to one org (empty = every require line).
	module_prefix?: string @go(ModulePrefix)
}

// #ModulePinsInput adopts a set of module pins from a source go.mod into every
// module a glob matches, then tidies.
#ModulePinsInput: {
	// mode: sync edits+tidies; check only asserts.
	mode?: or(_checkSyncWord)
	// source_go_mod is the go.mod whose require pins are the source of truth.
	source_go_mod: string @go(SourceGoMod)
	// glob is the directory glob (relative to the working dir) selecting modules.
	glob: string
	// keys are the module paths whose pins are adopted (e.g. sdk, spec).
	keys: [...string]
}

// #PruneInput garbage-collects merged-upstream session worktrees + branches: the
// session-scoped `.worktrees/<slug>/<repo>/` linked worktrees, the umbrella
// root's own worktrees, and the local `feat/` branches they pinned — across the
// umbrella root AND every submodule. A branch is prunable when its work is
// already on the merge target: its tip is an ancestor of `base`, OR its PR is
// MERGED and its tip is contained in the merged PR head (the squash-merge case
// git ancestry alone cannot see). A branch with commits BEYOND a merged head
// (unmerged local work) is NEVER pruned. Domain-neutral: the repo and merge
// target come from the authored input, never baked in (R3 / boundary law).
#PruneInput: {
	// mode: report lists what would be pruned (a dry run); prune performs it.
	mode?: or(_pruneModeWord)
	// base is the merge target a branch must be merged into (default origin/main).
	base?: string
	// include_closed also prunes worktrees/branches whose PR is CLOSED (abandoned).
	// Off by default: a closed branch may hold unmerged work.
	include_closed?: bool @go(IncludeClosed)
	// local_only skips the GitHub PR-state lookup. It then CANNOT see a
	// squash-merged branch whose remote ref still survives (git ancestry alone
	// misses it), so it prunes strictly less — never more.
	local_only?: bool @go(LocalOnly)
}

// --- vocabulary tables (S3) --------------------------------------------------
// Every closed set of words is a table keyed by word, and its word list is
// derived from the table and used as `or()` of that list (§10.1 S3, D-PAT-3).
// The table and the enum it feeds therefore cannot disagree, and a new word is
// ONE row here. `@vocab` names the owner (§3.1): these sets are compiled into THIS
// plugin, so the owner is the plugin's own word `task`.
//
// THREE tables cover FIVE `mode` fields: `_checkSync` is the one two-word set that
// three verbs share, so it has ONE home and three consumers rather than three
// copies that could drift apart.
_submodulesMode: {status: {}, bump: {}, verify: {}, currency: {}} @vocab(task)
_submodulesModeWord: [for w, _ in _submodulesMode {w}]

_checkSync: {check: {}, sync: {}} @vocab(task)
_checkSyncWord: [for w, _ in _checkSync {w}]

_pruneMode: {report: {}, prune: {}} @vocab(task)
_pruneModeWord: [for w, _ in _pruneMode {w}]

// ---------------------------------------------------------------------------
// The `kind: task` ENTITY body.
//
// A task is a named, host-native, REUSABLE plan — the SAME #Step grammar a candy's
// `plan:` uses, plus task-level execution config (workdir, env, vars, incremental
// staleness, dependencies, parameters). It is the declarative replacement for a repo
// task-runner entry: any task a repository needs — build, test, lint, release,
// verify, notify — is authored as a `task:` node in charly.yml and run with
// `charly task <name>`.
// ---------------------------------------------------------------------------

// #Task — one generic task entity.
//
// The first three fields mirror the Go Task runner's authoring surface
// (dir/env/vars/depends_on), the next block its incremental/staleness model
// (sources/generates/status/preconditions), then the execution knobs
// (silent/interactive/platforms/timeout/continue_on_error), then typed params,
// and finally the ORDERED `plan:` — the reused #Step grammar whose steps run in
// authored order on the host (or a venue the step selects).
//
// `description!` is required (the ADE identity contract every entity carries).
// A `plan:` is optional in the schema; the plugin's own OpValidate requires at
// least one step so `charly task <name>` has something to run.
#Task: {
	// --- identity (required: ADE) ---
	description!: string & !=""

	// --- execution context ---
	// dir — the working directory every step runs in. Relative paths resolve
	// against the task's project root (the directory holding charly.yml);
	// ${VAR} references resolve against env/vars at run time.
	dir?: string & !=""
	// env — extra environment variables exported for the task's steps. PATH is
	// reserved (use the shell profile); values are Go-coerced scalars.
	env?: {PATH?: _|_, [string]: schema.#StrVal} @go(Env,type=map[string]string)
	// vars — task-local ${VAR} substitution values (like a candy's var:), a
	// build/run-time map of string values.
	vars?: {[=~"^[A-Z_][A-Z0-9_]*$"]: schema.#StrVal} @go(Vars,type=map[string]string)

	// --- dependencies ---
	// depends_on — task names that must complete (in order) before this task
	// runs. `charly task <name>` runs the closure; a cycle is a load error.
	depends_on?: [...schema.#EntityRef] @go(DependsOn)

	// --- incremental / staleness model (Go-Task parity) ---
	// sources — glob paths whose modification invalidates the task.
	sources?: [...string]
	// generates — glob paths the task produces; if all are newer than every
	// source, the task is considered up to date and skipped (unless --force).
	generates?: [...string]
	// status — up-to-date probe commands; each is a shell command whose exit 0
	// marks the task up to date (all must pass for a skip).
	status?: [...string]
	// preconditions — shell commands that must exit 0 before the task runs; a
	// failing precondition aborts the task (unlike status, which skips).
	preconditions?: [...string]

	// --- execution knobs ---
	silent?:           bool
	interactive?:      bool
	platforms?:        [...string]
	exclude_platforms?: [...string] @go(ExcludePlatforms)
	timeout?:          schema.#Duration
	continue_on_error?: bool @go(ContinueOnError)

	// --- typed parameters ---
	params?: {[string]: schema.#TaskParamSpec}

	// --- the plan (the reused grammar) ---
	plan?: [...schema.#Step]
}

// #TaskValue — the HOST-SIDE value gate def for the `task` kind (the #Candy /
// #<Kind>Value analogue). @go(-): the Go type comes from #Task directly, so
// this def is validation-only. Its NAME is the contract the `kind` capability
// declares (plugin.go's InputDef), which is how the HOST resolves the gate for
// this kind without carrying any knowledge of it.
#TaskValue: #Task @go(-)
