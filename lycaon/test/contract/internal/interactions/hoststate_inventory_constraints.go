package interactions

// Production sources that hold a reader fact to one value class on a
// recipient. A recorded fact observed with two classes fails as stale.
const (
	srcCommandSticky        = "config/packs/painted-wolf/*/tools/profiles: command is sticky in every shipped profile, so it is never loadable"
	srcTerminalOpenSticky   = "config/packs/painted-wolf/{platform,implement}/tools/profiles: terminal_open is sticky wherever terminal tools are admitted"
	srcCoordinatorRole      = "internal/prompts/reader_facts.go:buildReaderFacts sets isCoordinator from the coordinator profile or a surface"
	srcWorkerRole           = "internal/prompts/reader_facts.go:buildReaderFacts: a worker profile has no surface, so isCoordinator is false"
	srcCoordinatorOnlyFacts = "internal/prompts/reader_facts.go:fillCoordinatorReaderFacts runs for the coordinator only"
	srcWorkerOnlyFacts      = "internal/prompts/reader_facts.go:fillWorkerReaderFacts runs for workers only"
	srcSpawnRoster          = "internal/session/readerfacts/resolver.go:Derive gives worker children no spawn roster"
	srcBranchView           = "internal/session/workerworkspace:OnWorkerBranch holds for worker children only"
	srcEditLimit            = "internal/tools/readcaps/caps.go:MaxMutationBytes"
	srcRunnerTimings        = "internal/toolcontract:ProgramInlineWait and ProgramParkThreshold"
	srcCoordinatorScopes    = "internal/prompts:CoordinatorPathScopesFromProfiles over the coordinator profile globs"
	srcCadence              = "internal/prompts/reader_facts.go:fillCoordinatorReaderFacts sets visual_review_cadence on_request"
	srcAskUser              = "config/packs/painted-wolf/*/tools/profiles: no worker profile admits ask_user"
	srcOrientation          = "config/packs/painted-wolf/*/tools/profiles: every worker profile holds pack_board sticky"
	srcRejectCodes          = "internal/prompts/reader_facts.go:fillWorkerReaderFacts recoverableRejectCodes over the shipped hint catalog"
)

var readerFactConstraints = map[string]map[string]string{
	RecipientCoordinator: {
		"agent_tools":                        srcWorkerOnlyFacts,
		"can_request_command_verify":         srcCommandSticky,
		"can_request_git_bisect":             srcCommandSticky,
		"can_request_git_commit":             srcCommandSticky,
		"can_request_git_read":               srcCommandSticky,
		"is_coordinator":                     srcCoordinatorRole,
		"native_edit_limit_mib":              srcEditLimit,
		"on_worker_branch":                   srcBranchView,
		"read_globs":                         srcCoordinatorScopes,
		"recall_may_widen":                   srcCoordinatorRole,
		"reject_codes":                       srcWorkerOnlyFacts,
		"runner_inline_wait_seconds":         srcRunnerTimings,
		"runner_park_seconds":                srcRunnerTimings,
		"tool_profile":                       srcCoordinatorRole,
		"visual_review_cadence":              srcCadence,
		"visual_show_terminal_needs_request": srcTerminalOpenSticky,
		"write_globs":                        srcCoordinatorScopes,
	},
	RecipientWorker: {
		"agent_tools":                        srcWorkerOnlyFacts,
		"can_ask_user_secret":                srcAskUser,
		"can_orient":                         srcOrientation,
		"can_request_command_verify":         srcCommandSticky,
		"can_request_git_bisect":             srcCommandSticky,
		"can_request_git_commit":             srcCommandSticky,
		"can_request_git_read":               srcCommandSticky,
		"can_spawn_implementer":              srcSpawnRoster,
		"can_spawn_web_research":             srcSpawnRoster,
		"can_spawn_workers":                  srcSpawnRoster,
		"card_edit_deferred":                 srcCoordinatorOnlyFacts,
		"card_may_dispatch":                  srcCoordinatorOnlyFacts,
		"card_may_edit":                      srcCoordinatorOnlyFacts,
		"card_may_run":                       srcCoordinatorOnlyFacts,
		"card_only_tools":                    srcCoordinatorOnlyFacts,
		"execution_mode":                     srcCoordinatorOnlyFacts,
		"is_coordinator":                     srcWorkerRole,
		"lane_s_sync_tools":                  srcCoordinatorOnlyFacts,
		"native_edit_limit_mib":              srcEditLimit,
		"read_globs":                         srcCoordinatorOnlyFacts,
		"recall_may_widen":                   srcWorkerRole,
		"reject_codes":                       srcRejectCodes,
		"runner_inline_wait_seconds":         srcRunnerTimings,
		"runner_park_seconds":                srcRunnerTimings,
		"surface_card_label":                 srcCoordinatorOnlyFacts,
		"surface_card_rule":                  srcCoordinatorOnlyFacts,
		"surface_id":                         srcCoordinatorOnlyFacts,
		"surface_publishes_text":             srcCoordinatorOnlyFacts,
		"visual_review_cadence":              srcCoordinatorOnlyFacts,
		"visual_show_terminal_needs_request": srcTerminalOpenSticky,
	},
}
