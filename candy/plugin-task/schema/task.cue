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
