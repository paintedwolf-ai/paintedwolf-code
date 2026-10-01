# Den notices

The in-app notice stack: four tiers escalating by how much the user can still do, the condition-to-tier partition, and where a notice renders once its tier is decided.

**See also:** [Den](den.md) · [First run](first-run.md) · [Host contract](host-contract.md) · [Accessibility](accessibility.md)

**Machine truth:** [`notices/`](../lycaon-den/src/notices) — `notice-store.ts`, `notice-select.ts`, `notice-routing.ts` · [`NotificationStack.tsx`](../lycaon-den/src/components/NotificationStack.tsx) · `notification-tier-invariants.test.ts` · [`critical-stop-model.ts`](../lycaon-den/src/notices/critical-stop-model.ts) · [`CriticalStopStage.tsx`](../lycaon-den/src/components/CriticalStopStage.tsx) · [`notice-actions.ts`](../lycaon-den/src/notices/notice-actions.ts) · host [`user-notices/`](../lycaon/config/packs/painted-wolf/platform/host/user-notices) and [`client-notices/`](../lycaon/config/packs/painted-wolf/platform/host/client-notices) catalogs

---

## Tiers

Four tiers, escalating by **how much the user can still do**. Pick the tier from that question alone, not from how bad the condition feels.

| Tier | Surface | Use when | Dismissable |
|------|---------|----------|-------------|
| Line | `NoticeRail` | Something failed but the app works; often transient | Yes |
| Card | `SystemNudge` | A capability is unavailable or a suggestion is worth acting on; the app still works | Yes |
| Stage | `StageErrorBoundary` → `CriticalStop` | **One view's render threw**: that subtree is dead, everything around it works | No; it replaces that stage until reloaded |
| Window | `CriticalStop` | **There is no using the app to fix it**; engine down is the bright line | No; it replaces the whole window |

**One condition, one tier.** A condition that surfaces at two tiers shows the same problem twice in two places that can disagree about how bad it is. The partition is pinned by `lycaon-den/src/notices/notification-tier-invariants.test.ts`:

| Condition | Tier | Not |
|-----------|------|-----|
| Backend unreachable (`offline`, browser fetch failure) | `CriticalStop` | Never a dock row: the HTTP boundary reports a browser fetch failure to the app connection state, which changes the window state before the rail can see it |
| Host-declared `tier: catastrophic` (e.g. `OS_BELOW_FLOOR` / `below_floor`) | `CriticalStop` | Never the Home card; `PreflightNudge` takes `non_catastrophic` only |
| Host-declared `tier: non_catastrophic`, `scope: app` (e.g. `BROWSER_ENGINE_UNAVAILABLE`) | `PreflightNudge` card on Home | Never a stop; the app still works |
| Host-declared `tier: non_catastrophic`, `scope: project` | Nothing; no probe declares a project scope (`test/contract/host/preflight_contract_test.go`) | Never the Home card, which cannot say *which* project it means |
| A stage's render threw (`VIEW_RENDER_FAILED`) | `StageErrorBoundary` | Never the window; only that subtree is dead |

**The host supplies the tier; Den never derives it.** `tier` is declared per resolution in the user-notice catalog and arrives on the preflight wire; the OpenAPI field says clients must not derive it from `status`. One code can resolve to two conditions with different tiers (`OS_BELOW_FLOOR` stops the app when the OS really is too old, but is only a missing capability when the version could not be read), so `resolveCriticalStop` and `PreflightNudge` both key on `tier`, never `status`.

The one exception is the offline stop, which Den decides: a `CLIENT_NOTICES` condition with nothing on the wire to read, since the host that would declare a tier is the thing that is down. Its copy is still catalog-authored (`host/client-notices/<kind>.yaml`) but code-generated into the bundle rather than fetched.

## The window stop

`CriticalStop` renders a `code` (machine id, also `data-code` and the e2e anchor), title, message, an optional host-authored remedy line, and the way out. Copy is never invented at the call site: host conditions render catalog copy off the wire, and the offline case reads `CLIENT_NOTICES.offline`. The remedy line is the catalog's `suggested_action`: for a host condition a standing remedy shown straight away, and for a start failure the escalation the stop holds until a retry has failed. That second form is how `engine_already_running` and `engine_not_started` avoid a generic note telling the user to reopen an app that would land on the same held lock, or the same absent engine.

**A named start failure replaces the generic offline stop.** When the managed backend refuses to come up for a reason Den can name, `SidecarStartFailure` selects a catalog entry and a recovery in place of `CLIENT_NOTICES.offline`: the held store lock (`engine_already_running`), the attach-only development build with no engine (`engine_not_started`), a startup the person stopped (`startup_cancelled`), and the four credential-vault states.

The vault is the one window-tier condition the user resolves **on the stop screen itself**. Saved credentials are encrypted at rest behind a device password, and the app cannot finish opening without it, so the stop renders a password field inside the stop card (one field to unlock, two to create) and its primary action submits that form. `credential_vault_locked` and `credential_vault_unlock_failed` unlock, `credential_vault_uninitialized` creates, and `credential_vault_corrupt` resets. Reset is destructive and says so before it runs: it clears saved credentials and leaves projects and conversation history alone, which the copy has to carry so a person who forgot one password does not believe they are about to lose their work.

**The way out is a closed set.** `criticalStopRecoveryLabel` is total over `CriticalStopRecovery` and answers `Try again` (reconnect), `Check again` (readiness recheck), `Restore snapshot`, `Restore from backup…`, `Finish restore`, `Finish starting fresh`, `Unlock vault`, `Create vault`, or `Reset vault…`. Adding a recovery makes the switch fail to compile until it has a label and an in-flight caption. A recovery snapshot is offered only when `health.status` is `recovery` and health carries both `recovery_snapshot_available` and `recovery_snapshot_at`; otherwise the stop opens the existing backup picker instead of offering a recheck that cannot change an incompatible store. A snapshot restore posts `/v1/backup/restore/recovery-snapshot`; a chosen backup uses the normal staged-restore endpoint. Both restart and reconnect the managed backend immediately. If that automatic finish fails, the staged marker and preserved prior data remain intact and the stop offers `Finish restore`; an incompatible snapshot moves directly to the compatible-backup path. The snapshot date renders absolute, never relative: the reader is choosing which day to go back to.

A stop carries at most one secondary action, chosen by recovery, not by code: a vault password stop offers `Forgot password? Reset vault…`; a store the build cannot open (the `Restore snapshot` and `Restore from backup…` recoveries) offers the lower-emphasis `Start fresh…`; a reconnect, or a recheck that is not the incompatible-store stop, offers `Save report bundle…`. `Finish restore` and `Finish starting fresh` offer nothing: an interrupted transaction has one correct next step.

`Start fresh…` requires the shared destructive confirmation, then posts `/v1/store/reset`. The host does not delete the refused store in place: it stages a clean current-schema store, preserves the refused database, sidecars, recovery snapshot, onboarding latch, and identity-coupled project/session files under the transaction's `.restore-recovery-<uuid>`, and applies the swap before the next store open. Provider definitions, credentials, approvals, and other device configuration remain in place. Applying the transaction clears store-dependent projections and the first-run latch, so restart opens initial setup. A failed automatic restart retains the marker and offers `Finish starting fresh`.

Recovery copy follows the structured health reason. `schema_mismatch` says the store uses a different data format; `integrity_failed` says the history failed its integrity check. Schema mismatch facts name both sides (`expected schema 1 · found schema 5`) and keep the host's raw mismatch in the selectable diagnostic block. The expected baseline is never mislabeled as the store's schema.

Which stop fills the window is `resolveCriticalStop` (`critical-stop-model.ts`), a pure function of sidecar status plus the readiness report. `lycaonFetch` is the sole REST transport boundary: an HTTP response, including an error response, proves the service is reachable; a browser-level fetch failure reports unreachable to `app-connection.ts`. An unreachable engine outranks a blocked probe, since readiness is served by the engine.

**It blocks the whole window, from one place.** The gate is a `Show` in `App.tsx`, above `Shell`: the nav rail and chat header are siblings of the stage host and the onboarding gate is Shell's outermost fallback, so a per-stage stop would leave all three rendering behind a screen claiming the app is stopped. `ChatDestinationPicker` lives inside that gate, so unmounting cancels a staged destination rather than leaving its portal over a stopped app. `ConfirmDestructiveHost` stays at the app-wide layer because recovery itself can require confirmation; `TextEditContextMenuHost` stays there so stop-screen prose remains copyable. Swapping the gated app out rather than overlaying it also tears down the global shortcut dispatcher and stops Shell's effects retrying against a dead engine. `notification-tier-invariants.test.ts` asserts the stages resolve **no** readiness stop of their own: no `createCriticalStop`, `resolveCriticalStop`, `CriticalStopStage`, or preflight read.

## A stage whose render throws

A throw inside a Solid computation fails silently. `runComputation` leaves the failed memo `STALE`, `markDownstream` skips observers that already carry a state, and no later store write re-queues it, so the view keeps its last frame while the store moves on underneath. Nothing is logged and no surface reports it; only unmounting the stage clears it, which is why the symptom is always "it works again if I switch views and come back".

Every stage body therefore mounts under its own `StageErrorBoundary` (`components/shell/StageErrorBoundary.tsx`): the current non-resident stage, each resident stage surface, and each chat surface. A resident surface stays mounted while idle, so one boundary around the whole stage column would let an inactive failure replace the active view too. The boundary renders `CriticalStop` with `code=VIEW_RENDER_FAILED`, catalog copy from `CLIENT_NOTICES.view_render_failed`, the error string as quotable facts, and **Reload view** (Solid's `reset`, which re-renders the children; a deterministic failure comes straight back rather than pretending to have healed). It also `console.error`s, because a stage that dies while nobody is watching still belongs in the log.

This is a *stage* stop: a failed render kills one subtree and leaves the nav, header, and every other stage working, so the copy says the rest of the app still works. A render path that throws on ordinary state is a bug to fix at the source; see [`host-contract.md` § Cache invalidation pattern](host-contract.md#cache-invalidation-pattern) for why transcript rows and the workflow run that spans them travel together, and `chat/transcript/projection/transcript-render-totality.test.ts` for the build that stays total.

## Readiness after boot

**Readiness is not only a boot fact.** A capability can fail, or recover, long after the window read its report: a provider is removed, the managed browser cache is cleared. The host says so on the device-scoped `preflight` SSE topic, carrying the probe id and no verdict; Den re-reads the report rather than patching a tier in from the wire, so one probe reads the same way whatever woke it.

**One readiness read, shared.** `platform/persistence/preflight-store.ts` holds the report; `markSidecarConnected` refreshes it once per connect, including an SSE reopen. The boot gate, Home's card, and both Settings panels read that store. An absent report is not a verdict: every consumer reads it as "nothing to say". A failed re-read keeps the last report, so a Settings re-check never reopens a boot gate.

**The stop carries facts even when nothing can be fetched.** A blocked probe arrives with the host's `catastrophic_detail` (allowlisted, machine-anonymous, `$HOME`-free; see the schema description), and the report bundle carries the same facts. Neither exists for the offline stop, which is the one users hit most, so `platform/stop-facts.ts` renders a client-side floor line from what Den already knows. Recovery health adds the refused store's actual schema to that line and distinguishes it from the build's expected schema. The facts line stays anonymous; the connect error's own text (Tauri appends an engine log tail to it) renders separately as a selectable diagnostic block on the generic offline stop, so a crash at boot is quotable without a bundle. The held-lock stop omits it: its copy already names the cause.

**A failed recovery reports on the stop, not a dock row.** The stop looks identical before and after a failed retry, so the click would read as doing nothing. `refreshPreflight` returns a structured outcome while retaining the last good report; `createCriticalStop` tracks that outcome and appends either the stop's held escalation or the catalog-backed failure explanation after a failed action, so a message must not pre-announce its own escalation. While an attempt is in flight the last stop is **pinned** and its action uses an operation-specific progress label (`Checking…`, `Restoring…`, `Finishing…`, `Starting fresh…`, `Unlocking…`, `Creating…`, `Resetting…`, one per recovery, from the same total switch as the labels): a reconnect sets the status to `connecting`, which reads as reachable, so an unpinned verdict would unmount and remount the whole app for the length of the connect. The *condition* belongs to one tier; the *outcome of an action* belongs to whatever surface took it.

**Copy names the failure, not the component.** "Sidecar", "backend", and "engine" name internals a user cannot act on, so shipped copy says what did not work (`Could not start`) rather than which part broke, and buttons say what they do for the user. `engine_not_started`, reachable only from a development build told to attach to an engine the developer runs, is allowed to name the engine because starting one *is* the fix; the command that starts it stays in the diagnostic block, so the card never carries a `./task` line.

## Scope: where a non-catastrophic condition renders

The tier answers *can the user still use the app*. Scope answers *where does this belong*, and the host declares it too: `scope` on the same wire fields, `app | project | session`, never inferred from the transport a notice arrived on.

| Scope | Renders | Rule |
|-------|---------|------|
| `app` | First in the shared notification stack, on Home and in the chat dock | An installation condition is visible wherever the user works with a chat or starts one |
| `project` | After app notices in the same stack, on Home and in the chat dock | Groups state **Project · name**; the name opens that project. One store entry backs every rendering, so one dismissal clears every copy |
| `session` | Only that chat's top dock | The narrowest scope; the thing you just did |

**One condition, one entry.** App and project notices render on Home and in the chat dock, never in the stage column beside a chat, and both copies read the same scope-keyed store entry. The focused onboarding gate carries its own setup guidance and does not mount the standing notification stack. `selectAppNotices` and `selectProjectNoticeGroups` build the standing stack; the latter orders the active project first and the remaining groups by their latest notice. `selectSessionNotices` keeps chat failures local. Sidebar counts point back to session-local notices instead of restating their copy.

**An action that failed where it was taken stays there.** Settings save, add-server, and import dialogs render catalog `title` / `message` / `suggested_action` through `noticeFromCaught` + `InlineNotice`. The dock is for conditions that outlive the dialog.

**The chat's dock is the only place a chat-scoped card mounts.** `ChatTopChromeStack` holds the order `session → spend → notifications → verify → protection → promote → provider → folder`, and `Shell.tsx`'s `chatTopDock` defines every slot's show rule. `notifications` is the same `NotificationStack` Home mounts. The stage column mounts it only while Home is the stage (`showHome()`). A card mounted inside `.den-chat-stream-body` would ride the scroller, so a card asking a question would leave the view the moment the reader moves; `notification-tier-invariants.test.ts` asserts that `ChatView.tsx` mounts none of these cards.

**Slot content is passed as thunks.** Both dock stacks take `{ present: () => boolean; children: () => JSX.Element }`. A slot reads `present` from inside a memo, so an eagerly evaluated object literal would rebuild the slot's content on every unrelated tick, discarding a card's `busy` flag mid-request or an answer the user selected but has not sent. Both stacks' tests pin the build count at one.

**A surface's failure is a notice, not its own line.** Stages and panes do not render their own error paragraphs, alert lines, or Retry cards. A read or operation that fails reports through `notices/surface-failure.ts` (`reportSurfaceFailure` for a caught failure, `observeSurfaceFailure` for one held as state), which publishes a project-scoped notice under the surface's stable `code` (repeats merge into one row) and keeps a typed host error's own copy. The region beside it renders nothing rather than an empty-state claim. Speculative work (the Files tree's read-ahead) reports nothing; the foreground read of the same rows reports its own failure. What stays inline: a value the person typed and is still correcting, state that carries real actions (the editor's disk-conflict banner, the file-operation queue), and progress. Admission control (`rate_limited`) is filtered out of the notice rail by `report-policy.ts`.

## Contextual cards

Home renders contextual cards in one column (`.home-view__notices`, above the idea composer): what changed, then what is missing, then a degraded capability. These are shared `SystemNudge` components driven by live conditions.

| Rule | Detail |
|------|--------|
| One contextual card per condition | Missing model config renders `NoProviderCard` on Home **and** in the chat dock, from one show rule (`notices/no-provider-card.ts`) |
| The rule names the gap, not a boolean | `providerConfigGap` returns `no_provider` or `no_default_model`, because the two halves need different instructions: an API key versus a choice of model |
| A card that states a condition stands down the probe | `PreflightNudge` takes `statedElsewhere` (probe **codes**, exact match) and reports the *next* candidate rather than going quiet, so a second, unrelated problem is still surfaced |
| Every card names the next step | The host's `suggested_action` renders on the card (`.system-nudge__hint`). Catalog `actions` name members of a closed vocabulary and Den turns each into a button: `open_ai_providers` navigates to Settings, `retry_contribution_frame` re-fetches and renders as **Retry**. Prompt recovery actions (`prompt_retry`, `prompt_keep_going`, `prompt_rewind_and_retry`) run in the chat the notice's session scope names, never whichever chat mounted last; a notice without a session scope, or whose chat is not mounted in this window, renders no prompt button. A destination that needs a Shell sink renders no button until that sink is wired. Den never parses remedy prose |
| The dock sets the page rhythm | The card's base margin carries the chat gutter for a transcript underneath; Home restates margins on the column so the cards line up with the page's own inset. Dark mode lifts them to `--den-surface` with Home's other cards |

**Scope state is not foreground state.** `notices/notice-store.ts` is keyed by scope and lives outside the app store, which holds the chat in view. It has a per-scope cap with a bucket LRU and supersedes by `(scope, code)`, so a flapping condition refreshes one row instead of filling a bucket. Nothing is persisted; a notice describes the live process. Home readiness-card dismissals follow the same process lifetime: they suppress the current `(probe, code)` pair until the app restarts.

## The notice catalog

Every host notice's copy is catalog data, one YAML per code under [`platform/host/user-notices/`](../lycaon/config/packs/painted-wolf/platform/host/user-notices), plus `defaults.yaml`. The count lives in the directory listing, not here. An entry carries `title`, `message`, `suggested_action`, optional structured `action` (or an `actions` list), and the `surfaces:` it is registered for; `use_defaults` entries inherit the three copy fields.

**A notice's buttons come only from its catalog entry.** The host adds no actions of its own, so an informational notice such as `user_image_not_visible` or `attachment_scanned_no_text` never offers to re-run the prompt. A turn-failure code declares its prompt actions as templates over `turn_progress`, which the host stamps only on prompt notices: `none` when the transcript shows the failed turn produced no assistant or tool output, `made` otherwise, including when the transcript cannot be read. `none` renders **Retry** (`prompt_retry`); `made` renders **Keep going** and **Rewind and retry** (`prompt_keep_going`, `prompt_rewind_and_retry`), because tools may already have run. The same code rendered for `http` has no `turn_progress` and so carries no prompt action. Validation renders every templated action for each scenario with each `turn_progress` value and with it absent, and rejects any result outside the action vocabulary.

**`surfaces` is the wire contract**: host validation and the user-notice contract test close every delivery surface, while `./task codegen:client-notices` generates the `host_error` enum and the separate bundled client-notice catalog.

Host conditions and transport-independent client conditions therefore always render catalog copy. Component-local loading state may carry local, deliberately scoped copy; an unclassified exception never renders its raw message and falls back to the bundled `unexpected_client_error` entry. Diagnostic evidence stays in its dedicated diagnostic or report-bundle path.

| Surface | Reaches the user as |
|---------|---------------------|
| `http` | The `Error` envelope (`code`, `message`, `details`, plus the rendered presentation fields) on a `/v1` call: the inline path, rendered where the action was taken. The overwhelming majority of codes. The HTTP status comes from the code's vocabulary entry ([host contract § 5](host-contract.md#5-error-handling)) |
| `host_error` | `NoticeCode` on the session wire: the dock rows and cards this page routes |
| `preflight` | `GET /v1/preflight` results, carrying the `tier` that decides card versus stop |
| `worker_failure` | A worker leg that could not finish, reported through the parent envelope |

There is a fifth state: an entry may declare `user_visible: false` and **no** surfaces at all. `session_aborted` is the one that does; a host abort the user asked for is not news to report back. Validation makes the pairing exclusive in both directions: a `user_visible: false` entry may not list surfaces or inherit defaults, and a visible entry must carry copy. Codes documented as never surfaced are still documented, so a code that quietly stops reaching anyone is a review finding rather than an absence nobody notices.

**Admission control is not a notice.** `rate_limited` keeps its catalog row because the host answers refused requests with it, but Den never shows it. Every 429 code (`rate_limited`, `file_operation_capacity`) carries `Retry-After` and means no effect ran, so `lycaonFetch` waits out `Retry-After` and replays the request, up to a bounded total. A refusal that outlasts the budget still throws `LycaonApiError`, and `shouldReportToNoticeRail` drops it.

A code may hold more than one surface: `workflow_active`, `workflow_not_runnable`, `project_mutation_in_progress`, `provider_not_configured`, and `worktree_stale` are both `http` and `host_error`, because the same condition can block a REST call *or* surface mid-turn. Messages are pongo templates: `provider_overloaded` renders the provider id and HTTP status when the host knows them and falls back to a target-free sentence when it does not.

### `host_error`: the session notice roster

The codes on `NoticeCode`, the one surface small enough to enumerate. Every one names a next step, because a notice with nothing to press is a dead end. Titles are the catalog's; a templated title shows its default.

| Code | Title | Condition |
|------|-------|-----------|
| `prompt_failed` | Couldn't finish that prompt | The turn stopped before a reply was ready; the detail, when there is one, is interpolated |
| `host_fault` | Painted Wolf Code hit an internal error | The host could not record a tool call's outcome and stopped the turn. The call is named, and the copy says its changes may stand when it ran. Resending is not offered as the fix ([tools.md § Tool lifecycle](tools.md#tool-lifecycle)) |
| `provider_unreachable` | Could not reach the model host | The request never got through. Nothing reached the model, so nothing was spent |
| `provider_server_error` | The model provider had a server error | The request arrived and the provider's own service failed it. Not the prompt, and not the local setup |
| `provider_silent` | Model host went quiet | The request was accepted and nothing came back. It may have run and been billed, the opposite of `provider_unreachable` |
| `provider_response_interrupted` | Model response interrupted | The response stopped partway through |
| `provider_overloaded` | Model host is overloaded | HTTP 503 after the host's own wait-and-retry |
| `provider_rate_limited` | Model host quota exhausted | HTTP 429 after automatic retry; the quota is still exhausted |
| `provider_request_rejected` | The model provider rejected the request | The provider refused the request itself; a known reason (such as a plan requirement) selects a specific title |
| `provider_not_configured` | No model configured | The session has no assigned provider, or the named one is not set up |
| `model_refused` | Requested model unavailable | The provider will not serve this model for this session; the pair is named when the host knows both |
| `provider_empty_completion` | Model returned no response | The model finished with neither text nor tool calls; the title says "Model reached its output limit" when `output_limit_reached` is stamped |
| `provider_context_too_small` | Model context too small | The prompt exceeds this model's context window on this host, so it would be truncated |
| `provider_tool_calls_unsupported` | Model can't use tools | The active model cannot make tool calls, so no agent action can run |
| `provider_tool_calls_in_prose` | Model's tool call was malformed | The model wrote a tool call as plain text that could not be parsed; the action did not run |
| `thinking_override_unavailable` | Thinking override cannot be applied | The saved thinking override does not apply to the active model |
| `user_image_not_visible` | Model cannot see images | The image is in the transcript but the active model takes no image input |
| `attachment_scanned_no_text` | No text found in scanned document | The attachment is scanned and carries little or no selectable text |
| `session_spend_ceiling_reached` | Spend ceiling reached | The session hit its budget; the figure reads "at least" when the host stamped the estimate a lower bound ([cost.md § Estimate coverage](cost.md#estimate-coverage)) |
| `grounding_escalated` | Session needs your input | The agent stopped after repeated grounding failures and needs a clearer direction ([open-agent-rules.md](open-agent-rules.md)) |
| `session_preparation_failed` | Chat preparation failed | The chat workspace could not be prepared |
| `project_mutation_in_progress` | Project is changing | A folder or draft lifecycle transition holds the project lifecycle lock |
| `workflow_active` | Workflow already running | A workflow is already active on this session |
| `workflow_not_runnable` | Workflow cannot run | The run is paused or already finished; the reason is interpolated when known |
| `worktree_stale` | Worktree missing | The chat's bound worktree is gone; choose Return to project folder in the Git tab to continue |

About half the roster routes the reader to **Settings → AI providers**: every model condition except `provider_unreachable`, `provider_response_interrupted`, and `prompt_failed`. A request that never left the machine is not a provider choice to revisit. That concentration is the point: the most common way a turn fails is the model, and the copy never blames the app for it. The converse holds too: when the app itself failed, `host_fault` says so and clears the model and provider.
