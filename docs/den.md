# Den

Den is the desktop client. It handles interaction, navigation, layout, and rendering while treating the sidecar as the authority for projects, sessions, workflows, tools, approvals, evidence, and durable outcomes.

**See also:** [Architecture](architecture.md#system-shape) · [Host contract](host-contract.md) · [Project space](project-space.md) · [Den session switch](den-session-switch.md) · [Den chat items](den-chat-items.md) · [Accessibility](accessibility.md)

---

## Client authority

Den may implement complex local behavior (focus, panes, shortcuts, drafts, virtualization, window placement) but never becomes a second source of product truth.

| Host authority | Client presentation |
|----------------|---------------------|
| Project/session identity and lifecycle | Focused project and visible stage intent |
| Workflow phase, gates, choices, and approvals | How those controls are arranged and focused |
| Tool outcome, process state, workers, and evidence | Activity-span projection, disclosure, animation, and navigation |
| Source bytes and editor-document durability | Editor view state, selection, tabs, and layout |
| Settings effects and contribution frame | Form drafts and local interaction feedback |
| Managed-secret reveal eligibility, challenge, and audit | Native user-presence prompt and ephemeral remasked presentation |
| Events and resource revisions | Cache invalidation, hydration, and rendering |

If a rendering decision requires two host projections, Den waits until they describe one coherent scope. It does not fill a gap by inspecting transcript wording or an older project cache.

## Store authority

Each domain has one client store:

| Store responsibility | Authority |
|----------------------|-----------|
| Navigation and stage intent | Shell store |
| Window/pane preferences and live resize | Layout store |
| Project metadata and roots | Project store, hydrated from host |
| Project session inventory | Project-session store |
| Cross-project attention | Device attention projection |
| Foreground session transcript and coordination state | Session app store |
| Connection, authentication, and SSE | Shared app connection |

The foreground session store is not the authority for project metadata. A project rename or root mutation updates the project projection once; chat, sidebar, Settings, and stages read that same result.

Persisted app state contains preferences, recents, and a bounded last-view snapshot. It is a startup hint, not a durable transcript or project database. Invalid local state resets to an empty client projection while the host remains authoritative.

### Source layout

| Path under `lycaon-den/src/` | Holds |
|------------------------------|-------|
| `components/shell/` | `Shell.tsx`, `ShellColumns.tsx`, window chrome, and the shell controllers |
| `shell/` | Layout store, pane-visibility intent, stage placement and scope, window claims, resize models, and the client-state invariant audit |
| `store/` | One store per domain: app state, projects, project sessions, recents, projection store, attention, cost, settings |
| `chat/` | Session switch and caches (`session/`), transcript projection and layout (`transcript/`), stream scroll and debug (`stream/`), visual artifacts (`visual/`) |
| `files/` | The Files stage, one directory per concern ([layout](files-stage.md#code-layout)) |
| `ui/` | Presentation and publication, surface queries, minimum visible hold, height-toggle motion |
| `notices/`, `shortcuts/`, `settings/`, `contributions/` | Notice stack, keybinding resolution, the settings registry, and the generated contribution frame |
| `platform/connection/` | Boot, backend and sidecar status, health, host identity, and request connectivity |
| `platform/persistence/` | Persisted app state, its parser, backup transfer, and the preflight store |
| `platform/windows/` | Item and editor windows, window channel, chrome, and backdrop |
| `platform/scrolling/` | Scrollport motion, frames, and reveal; themed scrollbars |
| `platform/` (other) | `desktop`, `files`, `interaction`, `navigation`, `harness`, and `semantic-driver` seams to the OS, host, and harness |
| `api/` | Generated wire types and the typed client; each `http-capabilities/<capability>.ts` holds one capability's interface and implementation |
| `components/<feature>/` | Feature surfaces such as `chatview`, `transcript`, `settings`, `scan`, and `search` |
| `*.css`, `styling/` | Stylesheet entries and their fragments ([Den styling](den-styling-tailwind.md#stylesheet-inventory)) |

### Shell controllers

`Shell.tsx` creates its controllers in its body, so their effects live under the Shell's owner. Each controller owns one concern: navigation state, pane visibility, workspace presentation and context, stage placement, search, commands, peer windows, session navigation, project lifecycle and removal, and contribution dispatch. A controller that reads Shell state declares it as a `Pick` of the type-only `ShellScope` (`shell-scope.ts`) or of another controller's result. The stage and chat columns are components with props in `ShellColumns.tsx`. Controllers retain their own interaction state and dispose their subscriptions with the Solid owner. Session navigation uses the shared generation fences and host hydration path; contribution dispatch submits the host's commands and publishes local view facts.

The removal controller requests host evidence and submits the person's explicit selection. It never enumerates other projects to decide which extensions are unused. Uncertain network responses retain the operation id for retry, and a successful project deletion retires its client projections even when optional extension cleanup fails. The review dialog disables retained or unknown entries and initially selects none. Project retirement clears file buffers by project identity, including when a deleted-project event arrives before the removal response or from another window; it does not depend on the deleted project's cached root list.

A chat is retired through one call, `EntityRetire.session` (`lifecycle/entity-retire.ts`), however Den learns it is gone: a delete in this window, a `deleted` session event from another window, or `session_not_found` from a resume, reconcile, or idle prefetch. Retirement ends source views addressed by the chat and forgets everything keyed by it: notices, attention, drafts, caches, the recents row, saved transcript positions and row heights, and the cold-boot snapshot. Navigation away from an open retired chat stays with the caller. A missing chat is recognized by the `session_not_found` code, never by a bare 404.

### Projection rules

- Host resource revisions and generations fence asynchronous hydration.
- Event handlers patch only complete payloads; otherwise they mark the resource stale and refetch.
- A rapid project/session switch uses a generation token so late responses cannot paint the new scope.
- Render caches replace rows by stable identity; timestamps do not define transcript order.
- Resident hidden stages retain local view state but release polling, focus reporting, and active subscriptions. Keyboard region entry never lands in a hidden surface, and each mounted chat registers its prompt recovery actions under its own session id, so a notice's action runs only in the chat that raised it.

SSE may collapse message snapshots and sort their rendering by transcript sequence. Replay checkpoints still advance only through the fully applied prefix of the original delivery order. Retries carry their cursor through that same ordered prefix; a failed handler reconnects without skipping unapplied events or reapplying successful deliveries.

Details: [Den session switch](den-session-switch.md) · [Den chat items](den-chat-items.md).

---

## Workspace model

The selected conversation is the ground of the workspace. Context surfaces (Files, Search, Security, Cost, Artifacts, Blueprints, Extensions) open beside or in place of that conversation without changing which session receives composer input.

Single and split layouts are two projections of the same stage model. A split contains the conversation plus one companion Context surface. Narrow windows may collapse the visual split while retaining the user's preferred layout.

Split order is relative to the sidebar and independent of workspace orientation. **Swap chat and context** exchanges the columns; **Mirror workspace** reflects the whole arrangement, including the sidebar and Files tree. One resolved order supplies the grid, resize direction, restore controls, and window-control clearance. Changing it preserves mounted panes, widths, drafts, scroll positions, and focus, and cancels an unfinished resize. The main window saves the preference; peer windows inherit it once and then keep their own layout.

Either split pane can be hidden from its edge control, Layout, the View menu, or its customizable keyboard toggle. One hidden-pane preference prevents both from disappearing. Hidden panes remain mounted and inert; navigation to a pane reveals it, and choosing a placement clears the hide. Explicit hiding wins over the narrow-window survivor. A single arrow restores an explicitly hidden pane; double chevrons claim room for a pane hidden by width.

Sidebar restore belongs to the adjacent visible pane. Controls sharing an edge carry **Sidebar**, **Chat**, or **Context** labels in physical order, with the sidebar outermost.

Below the width both columns need, the split shows one of them. Which one is declared, not inferred: it follows **Open at launch** (someone who opens into a split works in the stage; someone who opens into chat keeps the conversation), and **Settings › General › Too narrow for both columns** pins it. Neither column is closed: the pane the width took away keeps its state, and the seam offers the width back through the control described under [Responsive thresholds](#responsive-thresholds).

Going to a stage claims the chrome needed to use it. Automatic collapse caused by window size is distinct from a person explicitly hiding a rail; restoring a wanted rail may widen the window within the current display bounds. A claim in flight suspends the automatic decision: the viewport reports the pre-resize width until the window manager acknowledges, since deciding from it would drop a rail only to restore it a moment later. The request is recorded before the claim resolves: a layout change commits the single or split placement at once while the window widens behind it. Showing the sidebar paints the rail after the resize, but the open request is already recorded, so a second toggle reads it and hides the rail, which cancels the pending restore; Go to sidebar moves focus only if the rail actually opens.

The conversation's control shows a dot for the two states worth interrupting a hidden pane over, and its color says which: the accent tone while the host lists that session as waiting on the person (an approval or a question), and the positive tone while it holds a turn that finished after the person last read it. A running turn adds nothing, and neither does a failed turn, which is reported where the failure is. Reading the conversation clears the finished dot; the client marks the session seen only while it is genuinely readable (on screen in a focused window) and withholds that marker for the chat currently being read rather than flashing it for the round trip before the stamp lands. Explicit requests to see the conversation show it: Go to chat or composer, choosing, cycling, or starting a chat, opening its notification, and adding a selection to it. A narrow split that already shows one column offers the width control in place of a hide control.

The sidebar, the Files tree, and the split divider share one drag gesture. Dragging a pane below half its minimum width previews it hidden, releasing there hides it, and dragging back before release keeps it at its floor.

The divider sets the conversation's width in pixels, and the stage takes the rest. As the window narrows, the stage gives way first, down to one floor: the width its navigator needs beside its content. The sidebar folds, then the conversation narrows, and last the split falls back to a single column. Widening reverses the same order. That one floor is why nothing folds only to return: a stage keeps its tree for as long as it shares the host, so the column that survives the collapse is never better furnished than the column it replaces. A divider drag stops at the same floor, and Reset size restores the default conversation width.

### Responsive thresholds

A layout threshold resolves from the live box, never from preference math. Each region that changes width with the window (the split host, either split column, a browse stage, a file editor, an open tab panel) publishes the thresholds its measured content box satisfies as a band attribute, and the stylesheet selects on that band. The band is written from the same observation that reported the box, before the frame paints, so a threshold never lags the width that crossed it.

A preference may say what happens at a threshold, never where the threshold is. The split's surviving column is written on the host as a second attribute when the preference changes, so the stylesheet resolves *when* from the measured box and *which* from the declaration, and a continuous resize runs no script. Controls that depend on a band are mounted on both sides of it and revealed by the same rule: the seam facing the stage carries the hide control while both columns fit, and a double chevron below it (the same fit-collapse vocabulary the sidebar uses) which claims the window width that column needs and leaves every other pane as it is. On the stage's title bar that control is the conversation's, and it carries the same attention dot as the reopen control.

Whether the window can grow depends on its own state and the display it sits on, so the control makes the claim and reports what came back. A display with nowhere to grow (fullscreen, maximized, or already filling the work area) is answered where the claim was made: the control pushes once toward the edge that column would have returned from, and its label states the refusal, so a reader who never sees the motion still learns why nothing happened. Reduced motion drops the push and keeps the sentence. The refusal stands until the window next changes size, which is how leaving fullscreen re-arms the control. Both seam controls share one claim, so they share its answer.

Regions publish bands; anything smaller than a region (a card, a chip row, a chrome strip, a form) keeps its container query, because resolving a small subtree costs nothing. A size container re-resolves its whole subtree at every width, while a band invalidates only at the crossings. A stage nobody is looking at stops styling and laying out its contents while it sits idle.

The workspace registry records which project/session/stage contexts are on screen in each window. A split column counts only while it is legible: a hidden conversation, or the column a narrow window drops, is not published. The same on-screen fact drives seen stamps. A routed visit records only the stage it opened and the navigation it left. The origin's identity and label are resolved each time the "Back to …" action is read, so the action appears only while that origin is not visible in any window. Switching the chat inside a split therefore never produces a return to a conversation that is already beside the stage.

Each window is one client to the host, identified for the life of that window and never reused by another. Its event stream is what says the window is still there: host state a window holds rather than reads (an editing lease) is released when that client's last stream has been gone past its grace, so closing, quitting, or crashing gives the state back without the window getting a chance to say so, while a reload keeps it. Pulling a tab out builds its destination window before the drop, so a window whose gesture can no longer end (its initiator reloaded or closed) is discarded rather than left running unseen.

### Opening a workspace

A project workspace opens in one publication, including its sidebar.

The rail prepares behind the full-workspace loading cover as its project record arrives. Knowing the identity does not uncover navigation while folders, context entries, status chips, and chats are still arriving. The status chips belong to the project: trust, approval level, model, and whether cost tracking is on are project facts, read under the project's preparation group and mounted once per project, so a chat switch never remounts them. Only the cost cell follows the presented chat.

Stage and conversation columns prepare together under the selected project's preparation group, and so does the session's board with its git read, so the conversation's Git tab shows its change count on the frame the workspace paints. Each surface registers its required content synchronously; the group contains no DOM queries and cannot wait on another project's work. Once the required content or its explicit failure is renderable, the sidebar and columns publish together after a paint boundary. The cover spans the entire shell in either orientation. An opened workspace remains open; subsequent navigation exchanges the affected surface.

A workspace whose project the sidecar has not named yet opens the same way, one beat earlier. New project, open folder, and clone put the veil up at the user's commit, before the request that mints the record, labelled from the request itself. Home is not a surface between the outgoing workspace and the new one: it neither paints nor repaints while the registry learns the project, whether from the response or from the sidecar's earlier `created` event. The veil is keyed per open, so the pre-id cover and the project's own cover each mount already opaque rather than fading in over the other. Stage phases and the abandon path: [Project space](project-space.md#active-project-state).

### Chat list

The rail lists a project's chats in two groups. **Pinned** holds every pinned chat in the order the person set; a new pin joins the end, and pinned rows reorder by dragging within the group or with Move up and Move down in a row's menu. Dragging a row out of the list still opens the chat in its own window. **Chats** holds the first seven unpinned chats (`SIDEBAR_CHATS_CAP`) in the order chosen from the menu beside its heading: Created (the default), Last activity, or Title. The choice is device-wide app state and applies to every project. Pins never take one of the seven places. A selected chat outside both groups, such as one opened from All chats or search, is listed last so the selection stays visible without moving the rows above it.

Row order reads host facts: `pin_rank` for pins, and `created_at`, `activity_at`, or the title for the rest. Reading a chat, pinning it, or renaming it never moves it by activity, because none of those advance `activity_at` ([Session § Order](session.md#order-activity-record-changes-and-pins)). The sidebar asks for every pin plus twice the visible cap of unpinned rows (`SIDEBAR_CHATS_FETCH_LIMIT`), so a pin can fill the list before the host answers.

The list never reorders under the pointer. While the pointer is over it, or a row menu, the order menu, a drag, or an inline rename is open, held rows keep their places and take fresh content; a new chat joins the end and a chat that left the list leaves. When the pointer moves away, the list takes the host order and rows that change places travel there. The person's own changes apply at once: pinning, unpinning, moving a pin, choosing an order, and a chat they just created take their places immediately. Cycling chats with the keyboard follows the order on screen, pins first.

All chats sorts by Title, Created, or Last activity, and shows both times.

### Presentation and refresh

`ui/presentation.ts` defines preparation and publication. `ResidentSurface` composes preparation through Solid context, and `SurfaceDeck` retains section instances while their replacements prepare. Requested navigation, the candidate being prepared, and displayed content are distinct. Query records supply loading and error state; publication retains the displayed snapshot and rejects superseded candidates. A mounted surface is not automatically prepared: an interrupted first visit must still settle before it can replace the display. Diagnostic `data-presentation` and `data-presentation-pending` attributes expose the declared state to the harness; they are observations, never inputs to readiness.

Nested boundaries report content readiness before publication so the prepared group shares one paint boundary. Becoming visible does not restart acquisition: pending and active surfaces are both live. Files retains its tree during revalidation of the same workspace and roots. Project status chips retain their displayed values through same-project refreshes: a settings revision from the sidecar re-reads under the same project scope, and the previous value stays painted until the fresh one lands. Loading is never announced there; a read that fails renders its unavailable state on the chip itself.

File restoration reconstructs tab identities without joining every inactive document. The selected file joins first; after it is ready, only its immediate neighbors are warmed. Acquisition admits at most two joins, with at most one background join, so a new selection has capacity. Already-running joins and unsaved buffers retain their work. The outbox relay independently recovers pending operations even when their tabs are closed.

A validated restored checkpoint marks its existing history as preserved before remote or recovered command changes are applied. An unchanged reopen performs no checkpoint write. Native readers hold a shared store lease and read one atomically published file generation; writers additionally serialize by document. Independent documents do not share a writer queue. Capture, restore, and pruning take the exclusive store lease. Pruning runs outside request completion and skips a busy store; document lock files are operational data outside the backed-up outbox.

Files navigation publishes the requested editor as soon as its document and first viewport are ready. Tree discovery, indexing, and reveal do not block opening, switching, or closing tabs. With automatic reveal enabled, the tree follows the published file and retains its complete displayed rows until the destination is ready. Reveals use a 220 ms scroll with gentle acceleration and deceleration (`platform/scrolling/scrollport-reveal.ts`), respect reduced motion, and yield to direct input. Editor settings enable automatic reveal by default; disabling it preserves the tree position and selection during file navigation. Explicit Reveal in tree remains available from file tabs, editor content and gutters, breadcrumbs, review entries, image and file-info menus, and project path links. Directory failures settle as visible outcomes; superseded preparation cannot publish an older file.

Historical file selections use the same retained surface deck. The current editor stays painted until the first historical document publishes; later selections and comparison changes retain the displayed version until the replacement rows or an explicit error are ready. Superseded selections cannot publish, and returning to Current preserves the mounted editor.

During file navigation, the selected tab remains the command target and exposes its pending state through `aria-busy`. The retained document and its file summary keep their displayed identity. Loading adds no row to the document or changes its viewport geometry. Duplicate basenames carry distinguishing parent paths in the strip and open-file list, including accessible labels. The hidden-tab count reserves its digit width so scrolling does not resize the tab viewport.

Editor height and layout containment target `.cm-editor`. CodeMirror also copies its theme classes onto the document-level tooltip container; typography and colors can be shared, while editor geometry would give that empty container a full viewport of height.

Walk controls float by default. Dropping the grip at the editor's bottom edge docks the controls in a reserved row above its status bar; dragging away returns them to free positioning. With the grip focused, End docks and Home restores the initial floating position. The last explicit placement, including floating coordinates, is saved in device application state and restored across walks and restarts. A smaller pane clamps the restored controls into reach without overwriting the saved position. Editor settings offer a floating or docked default and a saved-location reset; changing the default also resets the saved location.

`createPresentationWaiting` supplies the shared grace period for query feedback, prepared bodies, and retained sections. Continuous pending work keeps one interval even when its internal phase changes. `createPresentationIntent` fences asynchronous UI completions; newer navigation, a scope change, or disposal invalidates earlier intent. Both snapshot publication and shell layout changes use this mechanism, so finishing an older save cannot redirect a newer selection.

| Transition | Presentation contract |
|------------|-----------------------|
| Cold open | Prepare required data and layout together. Delay waiting feedback by 300 ms (`PRESENTATION_LOADING_GRACE_MS`); a fast result does not flash a spinner or empty state. |
| Transient work | Work that starts and finishes faster than a person can read it holds its presentation for 1.6 s (`ui/min-visible-hold.ts`). A newer state replaces it at once; only removal waits, and a scope change drops what is held. File presence, Review's live rows, and the security full pass use it. |
| Same-scope refresh | Retain the successful result, controls, selection, and drafts while reading. Publish the replacement together. |
| Paging | Previous and Next keep their labels, enablement, and position while a page loads; only the page bounds disable them. The range and page labels describe the rows on screen, so they change in the paint that replaces those rows, and each reserves the width of its longest value (`StableLabel`). Scroll resets when the new rows publish, not on the click. |
| Discovery | A paged tree presents one immutable host presentation and reads its rows in those coordinates. A complete successor publishes with its destination frame when the person issues a command or direct scroll input settles, never mid-gesture. Discovery in progress shows no band, count, or provisional extent ([Files stage](files-stage.md#tree)). |
| Section change | Keep the outgoing section visible until the incoming section is prepared. Retained sections reactivate without remounting. |
| Split to full-width surface | Keep the outgoing split columns, orientation, and width constraints until the replacement surface publishes. Settings and other full-width destinations collapse the split at that same publication boundary. |
| Preference reload | Keep the current preference snapshot while its writes are queued or in flight; opening Settings must not reload an older saved layout over the user's latest selection. |
| Chat switch | A chat is an address, not a scope. Files and the surfaces that read the project's checkout are keyed by the resolved workspace, and the host declares when a chat's own worktree (`session_scoped`) makes it part of the address. Following a chat that shares the project checkout confirms the displayed snapshot (`retain()`) and reloads nothing; a chat with a different worktree holds the display until its workspace is ready, then publishes once. |
| Project/client change | A query may never display another project's or client's retained result. Cross-query retention requires an explicit common scope. |
| Failure | Failure is a renderable outcome, never an empty successful inventory. Preserve usable data; offer retry and keep navigation available. |
| Idle surface | Stop acquisition and subscriptions, suppress portaled UI and shortcut/focus claims, and retain local interaction state. Activity follows the entire ancestor chain. |
| Retained surface | A surface the deck keeps mounted behind the displayed one keeps its state but registers no command handlers, so page commands reach only the surface on display. |

`store/projection-store.ts` holds bounded records by resource identity. `ui/surface-query.ts` shares them by client and domain, joins overlapping reads, revalidates activation, and fences obsolete completions. Invalidation during a read requires a trailing fresh read; a command's published result supersedes an older read. Capture the addressed record before awaiting a command so navigation cannot redirect its result. A projection read has a 30-second deadline (`PROJECTION_READ_TIMEOUT_MS`) and retains the original error for structured notice handling. The deadline settles a failed read; elapsed time never certifies that missing content is ready. Requests that cannot be canceled still cannot publish after supersession or disposal.

Required means necessary to understand or operate the visible surface. Group independent reads into one projection when the surface needs all of them (blueprint launchers and their list, scanner configuration and its catalog, a cost report and its limits, a scan summary and its findings). Optional enrichment has its own boundary: the web index does not delay provider settings, and project overview indexing does not delay the file tree or workspace. Live transcript delivery and consequential controls continue under their host contracts; optional chrome never delays an approval or invents a terminal operation result. Backend revisions retain their domain-specific meaning; there is no universal workspace revision.

Settings configuration is scoped to its project while shared device settings remain shared. `settings/settings-draft.ts` keeps the acknowledged baseline separate from local edits, so a refresh or an older save reply cannot overwrite continued typing. Use `ShowLatest` or an unkeyed presence guard for a changing projection; use stable resource identity when a different entity must remount. Do not key an editor on the response object. `ResidentPortal` makes portaled surfaces follow their containing presentation, including inertness and focus suspension.

New asynchronous surfaces register with their nearest preparation boundary using `createSurfaceQuery` or `usePresentationParticipant`. Dialog bodies use `PreparedSurface`; section navigation uses `SurfaceDeck`. Domain-specific lifecycles such as source editors and live sessions keep their existing generation and operation contracts and declare their renderable readiness. Do not add local delays, global pending counters, or DOM polling to coordinate arrival.

## Git checkout and branches

The Git tab identifies the selected repository's checkout as **Project folder** or **Chat worktree**, alongside its current branch. Changed files, staging, commits, stash, pull, and push refer to that checkout. Repository selection stays separate from branch selection; a chat worktree for one repository never labels another attached repository as isolated.

The branch control is the single entry point for switching local branches, **Create branch…**, and **Create worktree…**. Switching or creating a branch changes the project folder shared by any chats using it. Creating a worktree gives this chat a separate checkout on a named new branch, starting from the current branch's latest commit. Existing staged, unstaged, and untracked changes remain in the project folder and are not copied. Other attached repositories keep using their project folders.

Creation dialogs name the repository, starting point, destination, and uncommitted-change behavior before submission. Branch names are explicitly entered rather than exposing a session UUID as the default. Cancel and Escape close the dialog and restore focus. Pending submissions cannot run twice; a failure retains the entered name and offers correction in place.

A bound checkout shows its base branch and commit distance. **Merge into [base]…** merges committed work into the project folder and keeps the chat in its worktree. **Return to project folder…** removes the separate checkout after confirmation and keeps the branch and its commits, including unmerged commits. The host refuses removal with uncommitted changes. A missing checkout exposes the return action even when Git status cannot load. Branch switching is unavailable while the chat has a worktree; checkout read failures show a retry rather than claiming an unbound state.

## Browse chrome vs Settings stages

| Surface | Purpose |
|---------|---------|
| Browse stage | Navigate and inspect project-scoped collections and detail |
| Settings stage | Configure device or project behavior through forms and lists |
| Chat stage | Show the addressed conversation, activity, composer, and transcript controls |

Shared chrome supplies title, actions, chips, and metadata. Feature pages contribute content; they do not introduce an alternate shell hierarchy. Stages are full-bleed workspace surfaces with consistent readable insets. Visited stages remain mounted to preserve selection and scroll, but inactive stages stop live work and reconcile once when shown again.

### Merged trigger groups

A run of adjacent dropdown triggers merges into one recessed track rather than standing as separate plates ([`DenTriggerGroup`](../lycaon-den/src/components/primitives/DenTriggerGroup.tsx)). The track is the control's plate; each trigger paints only its own state inside it, divided from its neighbours by a seam instead of a gap. Search merges Filter, Recent, and Export this way.

The track borrows the segmented slider's material, not its meaning. A slider's thumb marks the chosen member and travels to it; a trigger group has no chosen member, so **nothing travels**. The raise marks whichever popup is open and leaves with it, and a trigger holding non-default choices keeps the accent it wears anywhere else: elevation is transient, hue is durable. Controls that really are one-of-N belong in the segmented control, not here.

Membership is fixed. Every member renders on every pass and disables when it has nothing to offer (Search keeps Recent in the track with no history behind it), because a segment that comes and goes changes the track's width and seam count between renders. A control whose presence is genuinely conditional stays outside the track.

The group is one Tab stop, not one per member: [Focus baseline](accessibility.md#focus--reduced-motion-baseline).

## Open, find & context

Navigation uses typed targets: a project source coordinate, a search hit identity, an evidence or artifact reference, a stage with optional return context, or an external URL approved for opening. A path-looking string becomes interactive only when the host supplied a resolvable citation or navigation reference. Den does not turn arbitrary Markdown into filesystem authority.

In-view find runs within the currently focused surface and never searches hidden application state. Global Search is a separate host-backed feature. Context menus use the same typed target as the primary click, so Reveal, Copy, Open, and Add to chat cannot disagree about what a row represents.

Details: [Source navigation](source-navigation.md) · [In-view find](den-in-view-find.md) · [Context menus](den-context-menus.md).

### Files stage

The Files stage is a host-backed editor over attached roots. Den handles editing interaction and view state; the host is authoritative for document identity, source bytes, save conflict checks, watcher reconciliation, and agent-write collision policy.

Each root overview's **Agent context** is path-scoped runtime truth, not the project-wide trust inventory. Den asks `GET /v1/projects/{id}/agent-context` for that root and path, then shows only the applicable `AGENTS.md` chain and effective project skills from that root. Project configuration → Trust reports project-wide content and provides off switches; opening a project does not ask for permission. The sidebar trust chip and the project's single Trust group tab render the same host-owned comparison and file counts. Category links open that whole group using the shared Walk Git file list. Opening Trust clears its unread indicator while preserving the comparison; background refreshes update both views together without marking changes read. See [Project trust](project-overlay.md#project-trust).

Details: [Files stage](files-stage.md) · [Files live layer](files-live.md).

### Security stage

The stage opens on the project's findings. **Open** is what the scanners report that nobody has decided about, and **All** is everything the project has ever recorded, including what left and what it chose to ignore. Those two tabs, the filter box, one Filter control, and the overflow are the whole chrome. A run is one engine's answer for one input; it is the evidence behind a row rather than a mode, so it lives behind the overflow and behind a row's own "open the run that recorded this".

Every narrowing sits inside the one Filter control, each row carrying the count it would leave: a facet without one is a guess about what clicking it does.

The ledger's filters, ordering, and paging run in the sidecar. Den asks for a page of the project's set and shows what came back; sorting the rows it holds would rank a page and present it as a ranking of the project. Counts above the list are the project's totals and stay separate: fixed, not observed, and unverified are three different things, and Den never renders them as one "resolved" number.

Selecting rows offers the two things a person does with findings they have picked out. **Fix with agent** attaches each finding's file at its line and stages a draft; it never sends one, because a scan row is a reason to look and not consent to change the code. **Ignore** writes a decision to the project's committed `.paintedwolf/ignores.yaml` under `findings`, offering only the predicates every selected finding shares, requiring a reason, defaulting to an expiry, and showing the YAML before it lands. An export asks the sidecar to run the same query again, so a SARIF or OpenVEX document holds what the reader's filters match rather than the rows Den happened to have loaded.

Project configuration → Secrets switches between **Managed secrets** and **Ignored values** in one retained workspace. Ignored values lists declarations from `.paintedwolf/ignores.yaml`, their current status, exact bytes, reason and expiry. **Add value** writes one declaration to the chosen folder's file, a selected entry withdraws itself, and either action answers with the file's current declarations. The file stays the source of truth: it keeps its comments and its `findings` section, and **Open ignore file** reaches everything the page does not address. Contextual editor and chat actions add declarations through one review dialog. See [Ignored public values](secrets.md#ignored-public-values).

A full pass stays reachable while it runs. Findings take the stage as soon as the first scanner records any, so the empty state's progress gives way to the list; the coverage strip's full-scan control then reads how many of the pass's scanners are done and discloses every member's progress. It returns to Run full scan once the finished pass has had its minimum visible interval.

Copy about scanning never predicts a next scan, because nothing schedules one: automatic scanning is change-driven and an empty delta writes no scan record, so a scanner with no recent run has been watching a quiet tree. What a scanner's absence of findings proves comes from host-stamped facts (the coverage the scan established and whether its execution identity has moved), never from a Den inference.

Details: [Scan findings](scan-findings.md).

---

## Transcript and activity

Transcript truth is ordered by the host's immutable row order. Den materializes bounded windows, projects tool calls into universal activity spans, and virtualizes the DOM independently. Each chat scrollport has one transcript-wide virtual window. Workflow spans are row presentation metadata inside that window; they never create independent virtualizers, geometry spaces, or scroll adjustment loops.

### Row identity under streaming

One update projects host rows into display rows once, and every display row whose rendered data is unchanged comes back as the same object (`transcript-display-projection.ts`). A span whose rows all compare equal keeps its projection whole; inside a span that did change, each row compares structurally with the last build, and because a rebuilt row shares the wire objects it was built from, an unchanged row costs a few reference checks and never serializes. Row identity is what mounted components, memos, height estimates, and disclosure state key on, so a streaming reply re-renders only the rows it changed; rows the time pass adds follow the same rule.

Facts a row needs about the whole transcript are resolved once per update and read per key, never scanned per row: a row finds its own host message in a table keyed by id, the reviewable turn of each terminal assistant row comes from one pass over the display rows (`review-turns.ts`), a tool card's decision receipt compares structurally before the card re-renders, the worker roster and task matches compare by their card fields, and canonical artifact placement is resolved once for the transcript and shared with the visual surfaces. The chat view hands the transcript plain rows (`plain-message-rows.ts`): the store replaces a row whole, so reading each slot is the only subscription the projection needs, and no consumer pays a proxy trap per field.

### Spacing and geometry

Transcript spacing is defined once in `chat/transcript/layout/transcript-spacing.ts` (`TRANSCRIPT_SPACING`), as four rungs: `partGap` inside one row, then `rowGap`, `sectionGap`, and `turnGap` between rows. The same values publish CSS custom properties, seam insets, and content estimate inputs. Every length that separates or surrounds text is `rem`, because the root font size carries the composed accessibility text scale; a px seam freezes at scale 1 while the text around it grows. Virtual geometry needs pixels, so one published root size resolves the rungs for CSS and placement together. Content maxima (a visual's frame, a strip column) stay px.

`transcript-geometry.ts` owns column metrics and invalidation. Height-only content changes do not reread column typography. Width, fonts, text scale, and interior spacing select a measured-height generation; changing only row gaps or catalog insets retains measured interiors. Cache identities are derived from those inputs as one sampled snapshot after styles update, and include a build-time renderer fingerprint (a single digest; source files are never read in the scroll path), so spacing, card CSS, markup, bundled fonts, and dependency edits require no manual cache-version change. In development, hot updates publish the new generation after styles are installed. Persisted heights retain fractional pixels and belong to the current disclosure presentation. Hidden, animating, and font-loading rows do not publish reusable heights. Reading positions and disclosure state survive height-cache misses.

One row measurement coordinator collects mounts, mutations, disclosures, font completion, and resident reactivation. Its border-box observer delivers current sizes before paint; explicit reads share the scrollport's measure phase. A batch reads every dirty box before updating virtual sizes and publishes persistent height hints after the entire batch, so a hint cannot replace an old estimate before its correction is applied. Removed bindings and observations from another geometry generation are discarded. Resize corrections accumulate through the virtualizer and reach the physical scrollport together after the new extent is published, carrying the offset captured before layout so a native contraction clamp cannot apply the same movement twice. The applied correction remains authoritative, including native clamping, fractional movement, tail pinning, and thumb-drag ownership.

The measured transcript origin maps row coordinates into the padded scrollport. It is sampled on layout changes, never on scrolling. Each layout transaction starts at the latest captured or committed offset, including a restored position whose scroll event has not reached virtual rendering, so an earlier native event cannot undo a correction from the same frame. Markdown estimates account for tables, code blocks, lists, progress steps, and shared spacing; measured geometry remains authoritative. Navigation reveals coalesce through the scrollport's measure and render phases; a newer destination cancels an older pending reveal.

Presence is a host/presentation contract. A durable row is never withheld because its optional workflow or tool chrome is not hydrated. Live assistant content is a replacement snapshot until settlement. Tool rows use typed invocation, process, visibility, and presentation fields. Worker cards use worker/job identity. Grounding, checkpoints, and workflow boundaries remain distinct items even when visually adjacent.

Citation presentation distinguishes agent citations from **Host-added (observed)** references. When the coordinator omits citation metadata, the host can attach recorded sources to the unchanged answer without another model turn; Den discloses that attachment and keeps the sources inspectable. Grounding describes reference traceability and provenance, not a verified answer. See [Grounding](grounding.md#evidence-grounded-prose).

### Transcript viewport coordination

Each named scrollport (chat transcript, File summary drawer, and other themed hosts) has one physical offset and one application `commit()` writer in Den. A scrollport **is** its own viewport: the scrollbar layer hands OverlayScrollbars the marked element as its viewport, so the library generates no element and never reparents the children a component rendered; only its own chrome lives inside, and the layer rebuilds that if a render replaces the scrollport's content. Wheel, trackpad, touch, and keyboard input scroll that element natively; page code never consumes or eases them, and the innermost scrollport under the pointer claims direct input. Thumb drag, track click, tail-follow, reveal, jump, restore, and tail repin route through `ScrollportMotion` (`platform/scrolling/scrollport-motion.ts`). Transcript virtualization never commits an offset itself: it asks for index reveals and reports content that moved above the reading row, and the viewport applies both through `ScrollportMotion`.

Notched mouse wheels glide, and the smoothing lives below the page. Chromium and WebKitGTK animate wheel events in the engine; WebKit on macOS applies each notch at once and has no embedder switch for it, so the macOS host supplies the glide (`src-tauri/src/wheel_smoothing/`). A local AppKit monitor consumes a notch over web content and replays it as pixel scroll events that WebKit scrolls exactly like trackpad input: a critically damped approach (`SPRING_RATE` 32/s, landing within about 200 ms) covering the notch's whole distance (its line delta × `PIXELS_PER_LINE` 40 px, as WebKit would have applied it), with sub-pixel remainders carried so repeated notches keep their total. Steps are paced by the display link of the screen showing the web view and positioned for the frame's presentation time, so each composited frame carries one step. A notch near an active glide's origin extends that glide and keeps its velocity; a notch elsewhere glides on its own. Trackpad, Magic Mouse, and momentum events pass through untouched; a trackpad touch, click, or key press stops a glide where it is; wheels with Control, Option, or Command held and all wheels under Reduce motion pass through unchanged. Because the page only ever sees native scrolling, key-anchored corrections and `content_shift` compose with a glide without coordination. `cargo run --example wheel_glide_probe` drives notches through the monitor into a real `WKWebView`. At scroll-debug level 1, each finished glide logs `wheel-glide`.

| Command | When | Motion |
|---------|------|--------|
| `thumb_drag` | Custom overlay thumb | Direct `commit` each frame; cancels follow |
| `track_click` | Scrollbar track | Instant `commit` that centers the thumb at the pointer |
| `reveal` | Find, citations, approvals, Walk | `revealOffset` glide to virtual row geometry (instant under reduced motion, or when the caller asks for a jump); cancels follow |
| `jump` | Jump to latest, user send, resume | Glide (instant under reduced motion) to `tailOffsetY`; resumes following. A reader who is already following lands at once, because the pin carries a send and the chrome that follows it into view before paint, where a glide would suspend the pin and trail them |
| `restore_anchor` | Session reopen | `commit` to the saved `{rowKey, rowOffsetPx}` once its row is resident |
| `repin_tail` | Any geometry change while following, for every row kind and answer size | `commit(tailOffsetY)` before paint, to within half a device pixel. The tail is measured from the viewport's unrounded height, so chrome easing the viewport through fractional sizes keeps the latest line in place |
| `layout_compensation` | Scrollports outside the transcript that re-anchor their own layout | Restores an exact physical offset without canceling direct input; a virtualized list's row-size adjustment instead moves by its delta, as `content_shift` does; the transcript never issues it |
| `content_shift` | A row wholly above the reading offset changes size, or rows are added or removed above the reading row (older history, head eviction, regrouping) | Moves the offset and the engagement baseline by the same delta. Never cancels direct input. A thumb drag owns the absolute offset, so while one is active rows resize in place and the offset stays under the pointer |
| `tail_bound` | Offset below the painted content end with no live claim holding it | `commit(tailOffsetY)` after the tail holds still for a frame |
| `tab_inset` | Top runway added for an overlaying tab panel is withdrawn while the reader still sits inside it | `commit(0)`, only when the offset is within the retiring inset, so a reader who scrolled past it is not yanked |

A correction for content that moved is written as a **relative** scroll (`commitShift`), never as a position computed from the offset the main thread last saw. WebKit scrolls overflow on its own thread, so an absolute write replaces the thread's position with that older offset and discards the distance travelled since: measured in a `WKWebView` at 30 ms main-thread frames, an absolute correction lost 84–144 px of a trackpad gesture while the relative one landed exactly. The caller's measured offset is used for one case only, an offset the scroller can no longer hold (what a native contraction clamp leaves behind), which is then restored with the correction applied to it. Destinations (`reveal`, `jump`, `restore_anchor`, `repin_tail`, `tail_bound`, thumb drag, track click) stay absolute, because they are positions the application chose.

Application motion never uses a native smooth scroll. `behavior: "smooth"` hands the animation to WebKit's scrolling thread, and a write during it (a content shift, a clamp restore, the tail pin) leaves the page painted at one offset and hit tested at another, so hover and clicks land a row away until a later scroll brings them back together. `commit` always jumps; motion is a main-thread glide that writes each frame and yields to reader input: `ScrollportMotion.revealOffset` for a scrollport, `glideScrollLeft` for a horizontal rail. Proof: `platform/scrolling/native-smooth-scroll.test.ts`.

Scrollbar reveal follows the same split. Den owns overlay scrollbar auto-hide (`overlay-scrollbar-autohide.ts`; the library's own auto-hide is off because it reveals on every scroll event): a bar fades in for pointer movement over its host, direct input (native wheel, trackpad, touch, thumb or track gestures, scroll keys), then fades out `SCROLLBAR_IDLE_DELAY_MS` (700 ms) after input settles. Application commits (`repin_tail`, `restore_anchor`, editor scroll restore) never reveal a bar, so a transcript growing under a pinned tail keeps its scrollbar faded.

Scrollbar chrome never rides the content it scrolls. WebKit scrolls on its own thread, so chrome placed inside a scroller moves with the content until script translates it back on the next scroll event, and any main-thread delay shows as a jittering thumb. The chat transcript therefore hosts its scrollbar in `.den-chat-stream-frame`, outside the `.den-chat-stream` scroller (`attachThemedViewportScrollbar`), as the file tree and editor do. Where the engine offers a scroll timeline the handle rides the library's timeline animation over the native range; where it does not, as in macOS WebKit, `themed-scrollbars.ts` publishes the handle offset (`--os-scroll-percent`) itself, from the scroll offset it reads in the frame's shared read phase, and the vendored library's own scroll listener does not read the offset, because that read would follow CodeMirror's synchronous writes and flush layout again. The editor schedules that placement inside CodeMirror's own measure pass (`requestMeasure`), so a scroll frame lays the document out once for both; a scroll event that did not move the offset asks for no pass. The handle is sized (`--os-viewport-percent`) from live geometry whenever `ScrollportMotion` changes the retained extent (`subscribeExtent`) and whenever a scrollport reports a range change (`updateThemedViewportScrollbar`), so the thumb never waits for the library's deferred `update()`.

The project rail follows the same rule. `ShellNavRail` hosts its scrollbar in `.den-shell-nav-frame`, and its content element is the scroll extent (`extent` option): it grows to fill the rail but never shrinks below its rows, so the resize observation of it and of the viewport reports every range change without mutation probes. In the rail alone the handle sits flush with the edge (`--os-size: 8px`, no perpendicular padding), clear of the rows' 10px gutter, and its interactive area still reaches the shared 12px.

### Reading position and following

Each transcript has one reading position. While following, the scrollport is pinned to the latest message. Otherwise the position is the row under the scrollport top and the offset into it, resolved from virtual geometry and the motion controller's latest captured offset on each scroll frame, with no additional DOM read. Persisted view state per window and session stores that row key and offset (absent while following) plus user-set disclosure keys, never raw `scrollTop`, after an idle window; semantically unchanged snapshots do not rewrite app state. Reopening a session restores the position once its row is resident, loading older history when needed; when the row is gone it opens at the latest message, and reader input cancels a pending restore.

Every transcript disclosure (a tool row, its network detail, an activity span, the turn's diff group and each file in it, checkpoints, workflow feedback, grounding evidence, plans, and drafts) is identified by a key from [`transcript-disclosure-key.ts`](../lycaon-den/src/chat/transcript/presentation/transcript-disclosure-key.ts). Each surface has its own namespace, so two surfaces presenting the same record never share open state: a write's tool row and that file's row in the diff group both derive from the write's call, and opening one never opens or closes the other. The shared open state, motion holds, navigation reveals, row-height presentation, and saved view state accept only these keys. Markup and saved state are read back through the same parser, which drops any value no surface wrote.

Walk selection retains the command's tool-call identity across virtualization and disclosure changes. A collapsed ancestor receives the highlight; once its expansion settles, the selected command receives it instead. Collapsing returns the highlight to the containing card without changing the walk step or reopening disclosures. Navigation centers the visible target's summary, replacing the virtual row's coarse scroll target with the measured offset. Obsolete selections cancel their pending reveals; row remounts repaint the selection without starting another scroll.

Transcript delivery is classified from structured row kinds, not from generic DOM growth. A composer submission seated in the transcript, or the queue's Send action, resumes following and lands on the latest message once; its host echo and request completion preserve any later reader input. While following, prose, structural arrivals (activity spans, file edits, tools, progress, checkpoints, and other cards), and any other growth the reader did not cause stay pinned to the tail before paint. Hydration, remounting, task activation, and metadata-only updates do not create an arrival. Assistant prose renders all available markdown immediately, including tables and code; there is no presentation backlog, character clock, or replay ledger. A first nonempty prose delivery in the visible task may fade from readable partial opacity to full opacity over 160 ms while following the tail; reduced motion and text selection suppress that fade, and delivery fades never suspend the pin, reveal an answer's beginning, or write a scroll offset from a later frame. Entry fade is visual only: its animation end and safety timer can retire opacity classes, but neither may hold, pin, or catch up scroll. When no content or viewport geometry changes, the transcript stays at the same offset and visible row.

Following changes only on intent. The reader's own upward wheel, scroll keys, drag, touch, or thumb motion and transcript text selection stop following; scrolling back down to the tail resumes it, and downward wheel or scroll-key input at the tail resumes it when there is no remaining range to scroll. End resumes following through the transcript scroll controller and consumes the native key action. A selecting press (a mouse or pen press on transcript content, or any press once it has selected text) can release following but never resumes it, even when its drag autoscrolls to the tail. Explicit navigation stops following at once, even while the shell layout is settling or the transcript is hidden; only sampling the reading position waits for a readable layout. Pointer input clears earlier keyboard and wheel intent. Wheel and scroll-key input inside a tool output's own scrollport leaves the conversation's following state alone. Motion without reader input (layout clamps, content shifts, reveals) never changes following. Composer, dock, and checkpoint chrome change only the viewport's box: while following, the pin keeps the latest line in view as the viewport shrinks; otherwise the top stays where it is.

Opening a tool, activity, diff, checkpoint, or other disclosure holds the reading position before its first layout change. Following is retained only when the complete expansion, including automatically opened children, leaves the tail visible without moving the reader; otherwise later arrivals preserve that reading position until the reader returns to the tail, jumps to latest, or sends a message. Closing a card preserves the current follow state, and a disclosure toggled from the keyboard may contract the native range without changing follow. A pointer press keeps the control it pressed where it was: every height toggle declares itself to its scrollport, and changes that start during a pointer click join that press's anchor. A collapse also declares how far it will contract, and the scrollport reserves that range when the collapse starts: WebKit can land the contraction in a layout pass after every frame callback, so a clamp repaired from the next frame would already have painted one shifted frame, and a quick second click would land in it. Each frame of the motion measures the anchored shell once and carries the offset by its drift, so a sibling collapsing above cannot pull the control upward. Afterward the offset stays claimed, with the tail pin waiting, until the click sequence can no longer continue (the platform double-click interval after the last press), so a quick second press lands on the same control. The claim ends early when the reader scrolls or types, the application jumps, reveals, or restores, or new content reaches the offset. Reveals and jumps are never blocked by a disclosure in progress. `overflow-anchor: none` stays on the transcript scrollport.

This follows the interaction model in [StackBlitz's stick-to-bottom implementation](https://github.com/stackblitz-labs/use-stick-to-bottom): content growth and shrinkage retain follow, upward user scrolling cancels it, and an explicit jump restores it. Reading-position compensation follows the [CSS scroll anchoring model](https://www.w3.org/TR/css-scroll-anchoring/). Den owns that compensation because its transcript is virtualized and must behave consistently in WebKit and Chromium.

Geometry has one correction rule. A row wholly above the reading offset carries the offset with every change to its size (first measurement or re-measurement, in either scroll direction, one animation frame at a time), so the reader sees no motion. The reading row itself grows downward, and rows below never move the reader. Row-list changes above the reading row (an older page, head eviction, regrouping) keep the reading row by key: the virtual offset follows at once, and the scrollport follows in a microtask once those rows are in the DOM, before paint, so the painted tail cannot clamp the shift. Rows are measured in the frame they change, before paint; while the reader scrolls, newly mounted rows are measured by their resize observers rather than each forcing a layout. Older history loads while the reader is within two viewports of a resident edge (`LOAD_MORE_VIEWPORTS`).

### Extent and tail

Outside the transcript, `ScrollportMotion` publishes one synchronous DOM spacer for direct gestures and declared layout transitions, sized to the **offset ceiling a live claim needs** and recomputed from scratch every time. The spacer is sized by the range it measurably adds, not by its height: each write compares `scrollHeight` before and after, so an extent that fills a short viewport and a layout gap beside the spacer both resolve to the exact height within three writes. Only content that absorbs the spacer, adding no range however tall it grows (CodeMirror's row-flex scroller lays it beside the document), marks that binding as unable to retain range, which then reports its real range and logs `extent-hold-unsupported` at level 1; `e2e/scrollport-range-retention.spec.ts` probes every vertical scrollport in a real chat for that property. The terms are the current physical offset, held while direct input, a thumb drag, a tail pin, or a layout transaction is live, and a disclosure contraction reserved before its first animation frame. Range past the painted tail is only ever an offset the reader already occupies, never a high-water mark: under a live gesture a held spacer never shrinks, even once the reader is back on content, and it closes after input settles. While range is held, a wheel that would carry the reader further past the newest content is refused rather than corrected by a scroll write. While WebKit's scrolling thread is applying a wheel or touch stream, an offset write from the page, or the range growing and contracting at its end, can leave painting on one offset and hit testing on another, so every click lands a fixed distance from the pointer until the next scroll. The tail pin therefore writes nothing while a native stream is live; it lands once the stream has been quiet for 150 ms. Content can still contract at the end on its own (a virtual row measuring shorter than its estimate), so at that point an offset resting on the range end is written one pixel away and back, which re-seats the scrolling thread on the main thread's offset (WebKit ignores a same-value write). The same away-and-back write runs once more, at any offset, on the first pointer movement over the scrollport after a stream settles, in the measure phase of the next scrollport frame, so any split is re-seated before a press lands; a press is hit tested before any handler sees it. A new stream disarms it until that stream settles. `./task den:webkit:scroll` holds this in a real WKWebView. Past content height is never a term, because a spacer sized to a height the transcript no longer has is blank runway that keyboard, momentum wheel, and native reveals can all enter. Canceling application motion never withdraws the range supporting the current offset.

Transcript virtual runway is natural unmounted space only. Compound virtual rows (spans, diff rows, the diff manifest) estimate their complete collapsed presentation before measurement; persisted measured heights then refine that estimate by presentation key. A transcript image reserves its frame from the artifact's host-stamped `width` and `height` ([Visual surface](visual-surface.md#artifact-identity)), so a remounted image row lays out at its final height before the image decodes. Prose rows without a remembered height estimate from their content at the current column width (text length wrapped at the body font, plus each presented artifact at its host-stamped aspect ratio), so the scroll range and the thumb move little when they first mount. Anything that sizes a row renders when the row is created: a turn's Walk card reads the host's last answer for that turn, so a remounted row keeps one height. At scroll-debug level 1, a first measurement 24 px or more from its estimate logs `row-estimate-miss`, and a later change of 24 px or more logs `row-resize` with the parts whose heights changed (`transcript-virtualizer.ts`).

**The tail is content, never the retained extent.** `maxOffsetY()` is the widest offset the application may *address*; `tailOffsetY()` is the last offset that still shows content, with any synthetic spacer subtracted. Every "go to the bottom" path (`repin_tail`, `jump`, near-bottom and overflow tests, the `streamTailOffset` helper) resolves through the tail, and tail commits are clamped to it. A raw `scrollHeight - clientHeight` read is not the tail: while a spacer is published it overstates the bottom by the spacer's height, so a viewport pinned there sits in blank space and drops by exactly that height the moment the spacer is reclaimed, as a browser clamp delivered after the frame is painted.

The tail pin is a host policy (`setScrollportTailPolicy`), so it holds however and whenever the scrollport binds, including a rebind after a render replaces scrollbar chrome. The transcript's pin holds while following, unless a jump is gliding or Find is open. The scrollport reconciles it inside `notifyLayoutMutated()`, the pre-paint pass every transcript geometry change runs through, in either direction, so contraction and growth resolve in the frame that caused them. Touch and thumb gestures own the offset and suspend the pin while they last; wheel input does not, because upward wheel intent stops following before it scrolls. Upward scroll keys also release follow before native scrolling begins, so a pending geometry pass cannot pull Home or Page Up back to the tail. `ScrollportMotion` records `layout-contraction`, naming its host, from the pre-paint measure pass.

**Below the transcript is never a resting place, pinned or not.** `tail_bound` is the terminal reconcile: when the offset sits past `tailOffsetY()` and no live claim holds it (no human gesture, no direct-input settle window, no layout extent hold), the scrollport commits back to the painted content end. It is armed from the scroll sample, from the extent reclaim frame, and from a timer sized to the remaining direct-input window. Because the tail is measured live from `[data-transcript-end]`, the reconcile confirms the same tail across a frame before moving anything: a row that measures short for one frame, or a transcript still mounting rows, reports a moving tail and defers instead of clamping. Confirmation runs in the scrollport's shared measure phase (`scrollport-frame.ts`), so its read shares the frame's layout with the press anchor and row measurements, and a tail that moved becomes the next baseline without a second read.

### Seams and rows

**One seam for every row.** `.den-chat-stream-body` holds exactly one child, the transcript log, and every surface that reads as stream content is a row inside it: the virtualized transcript rows, including pending sends and their time markers, then a tail of non-virtualized ones (`SessionTranscript`'s `tail` slots) for a settled turn's outcome and a pending workflow-start proposal. **A seam belongs to the row beneath it**, as `padding-top` inside that row's measured box, selected by `data-seam` on `.den-seam`. Nothing spaces rows from outside them: the virtualizer carries no gap, so a seam survives a virtual window edge, never adds to a neighbour's claim, and drops out on the transcript's first row, which carries no `data-seam`. Tail rows carry no `data-index`, so measurement skips them. A surface parked beside the rows would need its own seam, and two seams disagree at the first row, which is why an optimistic send holds the seat its host echo takes.

**One classifier decides which rung a seam takes** (`transcript-row-seams.ts`), over the display rows rather than message ordinals, so a run bucket that reorders rows cannot mislead it. A prompt opens a turn and takes `turnGap`, unless the host recorded it as a `user_continuation` (a queue send the loop already took belongs to the turn it joined). A change of reading unit inside a turn takes `sectionGap`, as does either edge of a catalog span; consecutive rows of one kind take `rowGap`. A turn tail keeps `rowGap` above it and the next turn's seam below, so it reads as part of the turn it closes, and a day or unread marker takes the seam of the row it dates. Two claims on one seam resolve by precedence, never by adding up. The same classifier and rungs space the worker drawer, whose rows carry their seams for the same reason: `VirtualCardList` stacks a large list absolutely, where a container gap reaches nothing.

Turn footers use that same turn-opening classifier, including pending prompts. Each opening prompt reserves its footer immediately; host confirmation replaces the prompt in place, and clock delivery fills the footer's timing fields without inserting a row. A continuation stays in the existing turn. The arrival order of a prompt and its clock cannot move the preceding footer across a turn boundary.

### Composer, dock, and resize

The composer's height follows its body through one measurement per frame (`followBodyHeight`): a change pins the shell at its rendered height, applies its mutation, and every further change in the same frame reuses that pin; the next animation frame measures the body once and eases the shell toward it on the longest motion requested. A submission clears the draft and opens its activity lane without height motion, before the pending message's first paint. The same pending-send store that places the transcript row keeps that lane present until host activity takes over; delayed admission does not insert another lane.

The composer dock is a flex sibling of the stream wrap, not a scroll-padding overlay. Composer and bottom-dock growth shrink stream `clientHeight`; notification and shell chrome may change its available box. Resizes reconcile inside the resize observation: following repins before paint; otherwise the top stays. `--chat-stream-tail-clearance` remains on `.den-chat-stream-body` for the bottom fade band.

An open chat tab panel overlays the transcript and does not resize its viewport. The panel hangs from the chip row; notification rows keep their header height but ride the overlay's lower edge, so an opening panel slides them down rather than hanging below them. Only a transcript too short to clear the panel receives a temporary top runway, added and removed through the same pre-paint reconciliation.

Live resize publishes the latest pointer position once per animation frame and commits the final position atomically on release. Pane extents stay live throughout the gesture. Shared resize observation publishes the latest box per element in a reactive microtask batch before paint; the transcript viewport consumes the observed border box, including its padding, without another layout read. Changes to the virtual total share one reconciliation per animation frame; moving the mounted window within an unchanged total does not request another geometry read. Stream observers, motion, and virtualization share native offset capture on the scroll host before target rendering listeners, and a shared scrollport frame reconciles retained extent before running stream observers and publishing the latest captured offset to the virtualizer, so multiple events cannot repeatedly mount rows between layout reads. Nested tool scrolling does not update the outer sample. Geometry notifications never change following. Scroll geometry is cached only within an operation and invalidated after extent or editor writes.

### Scrollbar and editor measurement

Explicit scrollbar geometry refreshes share one pending-update queue. A host keeps its latest requested geometry while disconnected, during direct input, or under a measurement hold; reconnection and input/hold settlement wake eligible requests, and mutation observers never drain them synchronously. Each animation frame processes a stable batch and checks ownership and readiness again before updating; disposing a host cancels its pending work. Scroll listener delivery also uses a stable batch. Scroll-driven placement of scrollbar chrome reads all affected hosts before writing any of them, and direct input keeps one settle timer per scrollport and one recheck timer per host, so pointer and wheel traffic move deadlines rather than replacing timers. A retained-extent refresh measures only while a hold exists: within the natural range the platform clamps contraction itself and the clamp observer republishes the hold, so wheel input over settled content takes no layout read.

Automatic scrollbar discovery collects DOM changes without reading layout. A frame reconciles moved and removed hosts and constructs at most four new instances (`ATTACHMENTS_PER_FRAME`), yielding sooner when construction consumes its 4 ms allowance (`ATTACHMENT_FRAME_BUDGET_MS`). Idle resident surfaces retain existing instances asleep and defer new attachments until presentation; a sleeping instance wakes once for necessary repair and resumes its hold. Native overflow repair uses the library's published overflow state to select candidates, then batches dimension reads before any forced updates.

CodeMirror measures jumps beyond its rendered runway synchronously before paint. Native vertical scrolling within that runway shares the next editor measurement frame when the document and content geometry are unchanged; horizontal gaps, transformed editors, and ancestor scrolling retain synchronous measurement. Editor scrollbar dimensions are captured in CodeMirror's read phase and applied in its write phase, and the overview ruler joins the same measurement cycle, so scrollbar presentation never forces an extra layout between editor writes. Editor scroll positions are recorded from native scroll events, coordinated programmatic commits, and CodeMirror measurement phases; presence updates and file detachment reuse the snapshot instead of forcing layout. Historical document changes reconfigure the existing editor and its display compartments, retaining scrollbar bindings across versions and large-file display transitions. Editor hover follows rendered document positions and explicit gutter line numbers; read-range highlighting and restore controls do not measure screen coordinates on pointer movement. The file-tree pane publishes responsive width bands instead of invalidating its full subtree through a container query on every resize frame.

Source comparisons match lines before refining changed characters within a bounded foreground budget. Text highlights, authorship washes, and gutter cells are built for visible ranges. Equivalent comparison responses preserve editor paint; authorship changes reuse the text diff, and restore controls use the same chunks as the displayed comparison. Window and agent palettes share one cached recipe per document theme and contrast; a shared observer coalesces actual theme changes.

Filmstrip rows share in-flight loads and unpacked frame URLs by client, session, and artifact identity (`chat/visual/frame-archive-cache.ts`). Mounted readers retain their frames; released entries form an idle LRU bounded by 32 MiB of PNG bytes, 128 frames, and 12 entries, including pending loads. The first exceeded bound evicts the oldest idle entry. Deletion and session teardown revoke retained URLs and retire pending loads. Virtual row remounts reuse frames synchronously; a changed artifact never displays the previous artifact's frames while loading. Identical PNG bytes within a filmstrip share one URL and browser image cache while keeping separate step labels. Offscreen thumbnails load lazily and images request asynchronous decoding.

Transcript updates build message and tool indexes once, share the fresh projection across spans, and retain unchanged span output. A span is unchanged when its rebuilt rows compare equal structurally; the comparison stops at the first shared wire object or string, so an update never re-serializes the transcript. Shortcut labels, the dispatcher, the app menu, and the editor bridge share one resolved keymap per contribution frame, platform, and override map. File outlines are bounded per backend connection and keyed by project, session, root, path, and the server-reported analyzed SHA-256. Concurrent consumers share a request; the last canceled consumer aborts it.

### Debugging scroll

`VITE_DEN_SCROLL_DEBUG=2` or `localStorage.setItem("den:scroll-debug","2")` logs every `commit()` with source, offsets, and geometry. Level 1, on by default in `den:dev`, logs `content-shift`, `virtual-shift`, `row-estimate-miss`, `row-resize`, `range-change` (the transcript's scroll range changing, which is what moves the thumb), and `layout-contraction`.

Perf capture (`chat/stream/den-main-thread-perf.ts`) separates `loop-stall` from `loop-throttled`. The lag sampler cannot tell a busy main thread from a throttled timer, so a hidden or unfocused window reports under the second name. Treat a flat lag repeating at the sample interval as throttling, not jank. Full debug logging includes batched per-second synchronous operation counts, total duration, and maximum duration, plus a wall-clock/performance-clock anchor.

### Composer activity lane

The composer remains the addressed-session control surface while work is active. Its activity lane shows host state such as running turn, workers, parked question, approval wait, queued next turn, stop progress, or failure. The lane does not infer activity from an animated message or tool-output text. Stop, send, queue editing, and answering a parked question are separate actions with separate admission rules.

The composer's frame eases between heights instead of jumping. A draft that wraps, grows, or clears moves on a very short decelerating curve (`DRAFT_RESIZE_TIMING`, 20 ms), so a new line is open by the next keystroke, and a keystroke that keeps the line count does no motion work. The activity line moves on the transcript disclosure timeline (`HEIGHT_TOGGLE_MS`, 240 ms): it fades in once its lane is mostly open, keeps its settle before it leaves, and fades out of flow while the lane closes; activity that resumes while the line is leaving reverses it from where it is. The frame's contents ride its bottom edge, which keeps Send, Stop, and the line being typed in place while the frame moves. The frame is pinned before a change reaches layout, so the transcript gives up height along the same curve and never sees the destination early. A retargeted change starts from the current height. Notices and attachment chips land at their height directly; one that arrives while the frame is moving retargets that motion. Switching conversations, resizing the window, a hidden chat, and reduced motion land directly too.

Composer text sent while work is active joins the next-turn queue by default. The queue header's **Send** action moves its head item or linked head group into the current turn at the host's next safe boundary; it does not stop and restart the task. While that handoff is pending, the reserved queue is visibly marked as sending and its reorder, edit, link, pause, and remove controls are disabled.

Pending composer submissions choose their display surface before preparation or network work. An active session, an existing or held queue, or an earlier pending submission places the new entry directly in the queue with an admission indicator; it does not create a transcript row or move the reader to the tail. Host queue items replace matching pending entries by operation id and enable editing; rejection removes the pending entry. An idle session's first prompt retains its immediate transcript seat. Host messages remain authoritative when execution starts.

Send is replaced by **Cancel send** for as long as the reservation is outstanding. The card names what the wait is for rather than showing an indefinite "Sending": when a parked tool approval is holding the loop short of every send boundary, it says so, because answering that approval is what releases the message. Both facts are host state (the queue reservation and the pending checkpoint), not an inference from elapsed time.

A queue command that fails says so. Every refusal a person can provoke (nothing queued, a send already reserved, an edit against reserved items, a stale revision) carries its own code and copy and reaches the session's notice rail. Queue revisions increase independently per session. HTTP responses must match the current view epoch and advance its accepted revision before changing either the queue or pending transcript rows; switching away and back does not authorize an earlier response to repaint the session.

A message delivered by Send takes its transcript seat the moment Send is pressed, as the bubble the host will echo, outlined until the loop takes it. The reservation can wait on an approval for as long as the person takes to answer, and the transcript is where the person is looking, so the message is there rather than only in the popover. Once landed it is an ordinary prompt row. Cancel send withdraws the seat along with the reservation.

### Notifications

Notifications route durable or actionable conditions to the narrowest surface that can resolve them:

| Scope | Surface |
|-------|---------|
| App readiness or host failure | Application notice/critical stop |
| Project configuration | Project context or Settings |
| Session work | Composer/activity or transcript card |
| Worker approval | Parent workspace projection linked to the child worker |

Transient success belongs near the action that produced it. Persistent or blocking conditions remain visible until the host reports resolution.

Desktop notification adapters (`platform/desktop/notifications.ts`) distinguish explicit permission denial from unavailable native support or a failed permission query. The macOS bridge supplies delivery, session cancellation, and activation. Other desktop platforms use the installed plugin's delivery and permission operations; unavailable activation or cancellation is not synthesized in the client.

Details: [Den notices](den-notices.md).

---

## Blueprint card and review workspace

A Blueprint card projects a workflow-bound governing file and its approval state. Den reads the Blueprint through the host, displays review status, and submits explicit approve, supersede, revoke, or start actions with the current revision. The card does not decide whether bytes changed or whether the active workflow may advance; the host binds approval to content and projects the available controls.

The review workspace may show the Blueprint, plan-review evidence, workflow phase, and related artifacts together. These remain separate facts even when composed into one review experience.

---

## Approval surfaces

All approval-shaped interactions use one shell (`components/checkpoint/ApprovalShell.tsx`): reviewed action, origin and destination, consequence/reason, primary option, additional choices, and denial.

The card leads with the one fact the host lifted from the primary gate's citations (first contact with this host; what the chat has already read), then the impact line. The drop-up renders the host's fixed slots in host order with a digit on each row, the row's coverage under its title, and a disabled rung in place with the host's note rather than absent. Each row carries the digit of its rung (1 once, 2 day, 3 chat, 4 project or device); grouped choices such as a second subject or a quiet carry none. Digits 1–4 pick that rung from anywhere on the card, never a grouped choice; Enter approves the face; Escape denies. The face's coverage and expiry sit under the action row. A pending card shows how long it has waited from one minute on, and the dock badge counts the cards waiting on the person device-wide.

A high-risk card is marked with the band and a one-line consequence, but it takes the same single action as every other card: the face is enabled, Enter approves it, and the controls do not change. The label is reserved for actions that affect an account or a credential.

A secret card carries one extra line: the destination, and a chip naming what kind of receiver it is. The kind is a host fact (`model_provider`, `service`, `process`), and the model-provider chip takes the accent because that is the one destination that reads the credential rather than being authenticated by it.

Den renders option order, grouping, disabled state, coverage, and recommended face exactly as the host plan supplies them. It sends only the opaque selected option id or rejection. It never calculates a reusable grant.

Saved approvals list chat, project, and device authority and allow revocation. Chat approvals are grouped by chat and last until the chat is deleted. A decision chicklet and Settings operate on the same grant identity, so revoking in either place changes the next host evaluation. Above the list, Recent asks reads the local authorization ledger and shows, per reason, how many cards asked over the last week and how they were answered; where every subject in a row was one site, it offers the same durable host lease Settings can create. Counts are presentation and never change whether an ask occurs.

Completed approval decisions appear in the transcript that owns the tool result: coordinator decisions in main chat, worker decisions only in that worker's drawer. Pending worker approval asks remain available in the parent chat's approval dock.

Details: [Authorization](authorization.md).

---

## User rename (Den chrome)

Rename is one shared interaction over display fields. It never edits stable ids or filesystem paths implicitly. The primitive provides keyboard entry, validation, pending state, error restoration, and accessible naming; each entity supplies its host mutation and local projection update.

| Entity | Display consequence |
|--------|---------------------|
| Project/root | Sidebar, launcher, paths, and context labels update from project hydration |
| Session | Conversation title updates without changing transcript identity |
| Provider | Picker and Settings label update while provider id remains stable |
| Blueprint | Declared title/path behavior follows the Blueprint contract |

Optimistic paint is allowed only when the operation has one unambiguous rollback value. Otherwise Den waits for the host result and rehydrates.

Historical navigation follows the same rule: a requested version may be in flight, but the stage continues to paint the last settled comparison. Version bytes, diff facts, file identity, and any Walk playhead replace it in one publication.

---

## Session recovery + forgiving chat

Several actions can look like "undo", but Den keeps them visually and semantically separate:

| Action | Effect |
|--------|--------|
| Edit queued prompt | Changes a not-yet-claimed next-turn projection |
| Send queued prompt | Delivers the queue head into the current turn without canceling active work |
| Cancel send | Releases a reservation the turn has not taken yet and reopens queue editing |
| Stop | Interrupts current work and converges the session |
| Retry | Resubmits a failed operation with stable identity where supported |
| Rewind | Restores the boundary before a completed human turn |
| Git restore | Changes repository state through the git subsystem |

Rewind is offered only on eligible visible human prompts and only while the root session is idle. While a turn runs, Edit and Rewind stay in their row's toolbar but wait, and say they are available after the turn finishes; Copy keeps working. Turn state never mounts or removes row chrome, so starting or finishing a turn does not resize the transcript. Den previews affected files and conflicts before confirmation, submits the reviewed plan, and replaces affected projections after the host reports completion. Both Edit and Rewind restore the selected prompt and its attachments to the composer.

Stop does not retract persisted rows. Error does not mean user stop: the host's idle disposition distinguishes completed, user-stopped, and turn-error, and Den renders that field rather than inferring from the final assistant text. Retry does not reuse an old workflow revision without rehydration. Recovery controls belong to the message boundary they affect; their labels and confirmation copy state whether files, transcript suffix, workers, or only a queued draft will change.

### Transcript time

A chat says when things happened without stamping every row. Day labels, a clock time after a quiet hour, **New** since the person last looked, and a tail under each finished turn ("Finished 18m ago · Worked 3m 40s") are rows of their own (`components/transcript/TranscriptTimeRows.tsx`); a message's own time sits in its hover toolbar. Each settled tail stays visible. Worked time excludes waits on the person's decision. Placement: [Chat items § Time in the transcript](den-chat-items.md#time-in-the-transcript); the clock: [Session](session.md).

### Composer drafts and recall

Composer drafts are local interaction state keyed by the addressed session. Search or recall of prior work comes from host-backed results and inserts typed references; it is not merged into the draft as invisible authority.

Details: [Session](session.md#session-recovery-rewind).

---

## Settings

Settings separates device policy from project configuration. Project trust may admit only content the host can safely bound; it cannot install device authority.

### Finding a setting

`settings/settings-registry.ts` lists every individually addressable setting: a stable id, the row's label, the section and tab it lives on, an optional group, and the words people type for it. Rows render their label from the registry and carry its id as `data-setting-id`, so Crossbar and the page cannot disagree. Crossbar offers every entry as a go-to target once the query matches its label, context, or keywords.

Choosing one opens its section and requests a reveal (`settings/settings-reveal.ts`). The section's panel selects the tab, and the Settings view waits until the row is presented (mounted, and not under an inert, hidden, or aria-hidden surface), scrolls it into view, flashes it with the shared reveal flash, and focuses its primary control: the one marked `data-setting-control`, otherwise the first enabled control, otherwise the row. A request that does not land within eight seconds (`SETTING_REVEAL_TIMEOUT_MS`) lapses and leaves Settings where it opened. Host-provided lists (providers, MCP servers, trust surfaces, cache buckets) are reached through the registry entry for their list, not per item.

### Settings → Extensions

Extensions shows desired state, resolved effective units, installed packs, conflicts, validation, and project suggestions. Den submits mutations to the extension subsystem and then replaces its contribution frame. The UI does not reproduce Resolve algebra or write lockfiles itself. See [Extend](extend.md).

### Settings → MCP providers

MCP provider settings manage definitions, enablement, authentication, scopes, and consent fingerprints. A changed definition remains unavailable until the host admits the new generation. Project MCP applies when its device and project trust switches are on; it may select only device-configured loopback providers and cannot add remote endpoints or credentials. See [MCP](mcp.md) and [Project trust](project-overlay.md#project-trust).

### Settings → Security scanners

Scanner slots show installed definitions, readiness, selected engines, and rule-source status. Scan results remain project/session facts rather than Settings state.

### Settings → Cost

Cost settings choose price sources and warning/stop behavior. Den renders observational estimates and host-enforced spend conditions; it does not recompute provider billing semantics.

### Settings → General → Display

Appearance is device scope. Mode, theme choice, type scale, and layout preferences draw from the effective device contribution frame. Project content may suggest an extension but does not directly change the active theme catalog.

**Message times** shows a message's time on hover (the default) or below the bubble. Turn tails are not part of this choice: a tail anchors the turn seam and is always readable.

### Settings → General → Power

**Keep Mac awake while sessions are working** defaults on (`internal/settings/power.go`). The sidecar holds one macOS idle-sleep assertion while structured session work is active: a running session-tree turn, a host activity lease shown by the composer, a pending or running worker, or a pending or running security scan. Overlapping work shares one assertion, and disabling the setting or settling the final lease releases it immediately.

The host persists this state across projects and windows; Den only reads and updates the device preference and renders live assertion status. The assertion prevents idle system sleep, not display sleep, explicit sleep, or lid-close sleep. Unsupported hosts report the capability as unavailable rather than pretending the preference has an effect.

### Type

**Text size** is a device preference that multiplies the operating-system accessibility text scale rather than overriding it, so a reader who has already set the system slider keeps it and can still ask this one app for more. Interface and code fonts are chosen per role. Layout primitives use relative units and reflow rather than clipping fixed-height text. Contract: [Accessibility](accessibility.md#macos-text-size-signal).

### Settings → General → Updates

Update checks are on by default and can be turned off; each is a plain request for one static manifest that carries no device identifier ([Privacy](privacy.md#what-leaves-your-machine), [Updates](dev-tasks.md#updates)). Den displays current version, channel, check result, download/restart progress, and rollout eligibility supplied by the host/updater boundary. The client does not infer a newer version from release prose or start an update without a reviewed updater result.

An offered release remains installable after a failed recheck or an automatic check that holds back a newer release. Manual checks bypass rollout; subsequent automatic checks do not retract that same release. A successful manifest response with no update may withdraw it. Disabling checks invalidates in-flight results, and an older check cannot complete a newer check's state.

Installation waits until every live window has preserved its pending work and released its subscriptions. Input pauses during this boundary; preservation and acknowledgement failures retry automatically. A failed installation resumes the windows automatically. Ordinary application quit and window close use the same preservation behavior, with no outbox repair decision or forced-exit timeout. A window still opening must join preservation before installation or exit can proceed.

### Settings → Advanced

Advanced surfaces expose diagnostics, local data, backup/restore, credential cleanup, and other operator controls whose consequences exceed an ordinary preference.

Backup preserves the durable product store and referenced host data, but excludes credentials and behavior-installing device control-plane configuration such as MCP connections and extension setup. Restore is staged, validates compatibility, saves a recovery copy, and immediately restarts the local backend before normal work resumes. Canceling the native backup save dialog is a neutral outcome: no success or failure message is shown. Desktop backup success is reported only after the selected file is written. Cache clearing acts only on rebuildable data. Reset and uninstall behavior are described in [Privacy](privacy.md).

---

## Reports & What's New

**Report a bug:** diagnostics remain local until the human explicitly saves and shares them. The report surface identifies what the bundle contains and excludes credentials by construction.

**What's New:** release notes are shipped content keyed by version. They are presentation, not an update decision or compatibility contract. Missing notes do not block startup.

**Third-party software:** license notices are generated from the shipped dependency inventory and presented as a local document. See [Licensing](licensing.md).

---

## Accessibility

Every shared primitive implements its focus, keyboard, screen-reader, motion, and scaling behavior. Feature pages compose those primitives rather than restating accessible behavior per component.

Hover tips are Den's own layer, never the OS `title` popup, and a tip is never a control's accessible name: [Accessibility § Hover tips](accessibility.md#hover-tips). Type scale is the composition of the macOS accessibility text size and the device Text size preference; see [Type](#type) and [Accessibility](accessibility.md#macos-text-size-signal).

Details: [Accessibility](accessibility.md) · [Keyboard shortcuts](keyboard-shortcuts.md) · [Den styling](den-styling-tailwind.md).

## Boot restore

Boot paints from bounded local hints, connects to the sidecar, then reconciles projects, the selected session, and live events. A stale snapshot may improve first paint but cannot survive contradictory host state.

When Den owns the bundled engine, boot renders host-reported semantic startup progress (`platform/connection/engine-startup.ts`) instead of guessing readiness from a deadline. Regular heartbeats keep a slow phase visibly alive. Missing heartbeats change the screen to a non-terminal `stalled` state with stop and report actions; only a structured host failure, child exit, protocol violation, or explicit stop ends the attempt. A long startup that continues to report progress also exposes stop and report actions without relabeling progress as failure.

First-run setup is a separate latch. An unavailable provider after onboarding becomes a recoverable configuration notice, not a repeated blocking wizard. See [First run](first-run.md).

## Quit

Quitting asks the bundled engine to stop and waits for the engine's own exit rather than a timer sized independently of it. The engine's ordered shutdown is bounded (runner drain, HTTP drain, resource release, and a reserve the store keeps for its clean marker and WAL truncate) and it declares that worst case for the shell. The shell's graceful-stop wait stays above the declaration, so an ordinary quit during active work reaches a clean close instead of a hard kill; `engine_graceful_stop_exceeds_the_engine_shutdown_budget` (`src-tauri/src/sidecar/tests.rs`) pins the relation against either side drifting. An idle engine exits in milliseconds. A stop slower than a couple of seconds is recorded in the engine log with what ended it. The shell drains engine stderr through a bounded writer; each of the current and previous log files is limited to 4 MiB (`ENGINE_LOG_MAX_BYTES`) even when the sidecar runs continuously.

A hard kill is what the budgets exist to avoid: it skips the clean-shutdown marker, so the next launch runs a whole-store integrity and foreign-key check on its startup path, and any violation refuses the store into recovery mode.

An engine that exits while the app runs is the shell's to answer, not the window's to discover through failing requests. The shell reaps it, records how it ended, and starts a replacement on a new port with a new bearer; every window follows `engine-state`, keeps its workspace behind a `Restarting the engine` notice, and rebinds when the replacement runs. An engine that keeps exiting stops the window with the exit and a `Try again`. [Host contract](host-contract.md#supervision) defines the states and the restart policy.

## Invariants

- One client store serves each domain; no dual project or session catalogs.
- Source-tree membership is one workspace-keyed store shared by Files mounts; components control expansion, focus, selection, and scroll only.
- Host fields and revisions drive business behavior; transcript prose does not.
- Hidden resident stages keep view state but release live work.
- Typed targets drive navigation, context menus, citations, and Add to chat.
- A detail panel is keyed on its subject's identity, never on the object a refresh replaced; a background poll or a wholesale list refresh must not tear down the field the user is editing.
- Approval options and authority are host-authored.
- Live transcript projection remains separate from durable settlement.
- Local persisted state accelerates boot but never outranks the host.
