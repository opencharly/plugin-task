// schema/task.cue — the SELF-CONTAINED CUE schema for plugin-task's authored
// plugin_input. It defines ONLY the plugin's own input defs (command-level and
// verb-level); the `kind:task` ENTITY body is the base-schema #Task, validated
// host-side against #TaskValue (a task's `plan: [...#Step]` references the base
// #Step grammar, which a self-contained plugin schema cannot carry — see
// /charly-internals:plugin "Why self-contained schemas").
//
// SELF-CONTAINED: package-less and references NO base def (the load gate
// compiles it standalone AND splices it onto the base), so it may use only
// plain primitives.

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
	// mode: status lists; bump stages new gitlinks; verify asserts.
	mode: "status" | "bump" | "verify"
	// pinned_from is a checkout path whose gitlinks are authoritative for the
	// entries named in pin_map (e.g. "charly"). Empty => no pinned set.
	pinned_from?: string @go(PinnedFrom)
	// pin_map maps a submodule PATH in this repo to its twin PATH in pinned_from.
	pin_map?: {[string]: string} @go(PinMap)
	// rolling lists submodule paths that track their OWN default-branch HEAD
	// (every submodule not in pin_map is rolling by default).
	rolling?: [...string]
	// skip lists submodule paths the verb must not touch.
	skip?: [...string]
}

// #FileParityInput asserts (or syncs) that paired files are byte-identical.
#FileParityInput: {
	// mode: check fails on any drift; sync copies left -> right.
	mode?: "check" | "sync"
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
	mode?: "sync" | "check"
	// target is the file whose marked region is replaced.
	target: string
	// fragment is the file carrying the source region (must carry the markers).
	fragment: string
	// begin / end are the region marker strings (a line containing begin starts the
	// region; a line containing end closes it), matched with the fragment's own copy.
	begin: string
	end:   string
}

// #ModulePinsInput adopts a set of module pins from a source go.mod into every
// module a glob matches, then tidies.
#ModulePinsInput: {
	// mode: sync edits+tidies; check only asserts.
	mode?: "sync" | "check"
	// source_go_mod is the go.mod whose require pins are the source of truth.
	source_go_mod: string @go(SourceGoMod)
	// glob is the directory glob (relative to the working dir) selecting modules.
	glob: string
	// keys are the module paths whose pins are adopted (e.g. sdk, spec).
	keys: [...string]
}
