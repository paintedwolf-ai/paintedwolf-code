# Den session switch

How Den moves the user between sessions and projects without stale transcripts, duplicate SSE subscriptions, or races from rapid sidebar clicks.

**See also:** [Den](den.md) (store map + invariant catalog) · [Project space](project-space.md) (stage scope and render gates) · [Projects](projects.md) (`SwitchKind` semantics, launcher and welcome behavior) · [Den chat items](den-chat-items.md) (what the hydrated transcript renders)

**Machine truth:** `lycaon-den/src/chat/session/` — `session-switch.ts`, `session-lifecycle.ts`, `session-reconcile.ts`, `session-chat-cache.ts`, `session-chrome.ts`, `session-prefetch.ts` · `chat/transcript/projection/message-events.ts` · `components/shell/session-navigation.ts`, `session-creation.ts` · `platform/connection/app-boot.ts` · `shell/den-client-state-invariants.ts`

---

## Pipeline overview

```text
Shell (user action)
 → beginStageSwitch / beginWorkspaceOpening / clearToHome
 → runResumeSession | runCreateSession [session-switch.ts]
 → appStore chat hydrate actions
 → session-lifecycle hydrate fns
 → reconcileActiveScope (reconnect, or a resume served from the chat cache)
 → commitStageScope
```

Lifecycle modules **do not** clear chat; session-switch controls transcript boundaries.

**Two surfaces map the deps.** `sessionSwitchDeps` in `Shell.tsx` serves user navigation; `bootSessionSwitchDeps` in `platform/connection/app-boot.ts` serves boot, where there is no Shell yet. They are not interchangeable: Shell's `returnToHome` also resets nav and its `openSession` forwards `initialPrompt` for a Home idea submit, while boot's does neither. A change to the `SessionSwitchDeps` shape has to land in both.

Neither is the final word at the call site. Shell's mapping is a base that each caller spreads and overrides: both callers supply `prepareProject`, and the Home draft path replaces `openSession` and `completeChatSessionHydration` to buffer the commit while the draft stays visible. Read the call site, not only the mapping, when asking what a given switch runs.

The three branches (create, same-project resume, cross-project resume) are listed step by step under [runCreateSession ordering](#runcreatesession-ordering) and [runResumeSession ordering](#runresumesession-ordering).

---

## Generation token

| Symbol | Defined by | Role |
|--------|-------|------|
| `shellSessionSwitchGeneration` | `session-switch.ts` | Process-wide monotonic token |
| `generation.next` | start of each switch | Bumps counter; aborts the previous switch's signal |
| `generation.isLatest(n)` | after each await | Drop stale work |
| `generation.signal()` | captured at `next` | Handed to `hydrate` so a superseded bootstrap download stops |

**Rule:** every async gap in `runResumeSession` / `runCreateSession` re-checks `isLatest`. Rapid launcher clicks must not apply an older hydrate after a newer switch started.

**Hydrate apply fence:** `runResumeSession` passes `{ shouldApply }` into `hydrate`. Callers thread that predicate into `resumeChatSession` / `reconcileActiveScope` so **in-flight** network work cannot write `currentSession`, transcript, or unlock hydration after a newer switch. Checking `isLatest` only *around* `await hydrate` is not enough; a superseded hydrate must abort its own store writes.

**Superseded reads stop:** the context also carries the generation's `signal`, which the next switch aborts. Shell passes it to `resumeChatSession`, which hands it to `getSessionBootstrap` (and to `reconcileActiveScope` on a cache hit), so holding Cmd+] through a project downloads the chat it stops on rather than every chat it passes. A signaled bootstrap read joins an unsignaled read of the same session already in flight, such as the boot prefetch, and abandons only its own wait. Callers off the switch path pass no signal and keep sharing one in-flight request.

**Recents never gate a switch:** `resumeChatSession` and `enrichCreatedSession` update the recents row in memory and let its disk write settle behind the transcript apply.

**Create apply fence:** `runCreateSession` passes the same predicate into `create`. A create response may persist a server session after a newer navigation starts, but it must not bind `currentSession`, arm a workflow, or publish its scope.

Shell reserves a generation before waiting for a backend connection or creating a project. A newer navigation cancels that outer work before it can enter either runner.

---

## chatHydrationLock

| Action | Lock value | Effect |
|--------|------------|--------|
| `clearChatForSessionSwitch(lock?)` | the argument, or none when omitted | Clears messages/workers/`currentSession`; blocks parent SSE message apply |
| `beginSessionResumeSwitch(lock?)` | the argument, normally the target `sessionId` | Keeps transcript visible; still blocks cross-session SSE bleed |
| `completeChatSessionHydration` | clears lock | Composer + stream accept events again |

Both setters store whatever they are given, including nothing: omitting it leaves the lock undefined and hydration unlocked. `"*"` is not a default; it is what the cross-project resume branch and `runCreateSession` pass, and it means *no session may apply yet*, which is right when `currentSession` has just been dropped and the incoming one is not bound.

**The lock names the session it protects, and blocking is what protection means.** While `appStore.state.chatHydrationLock != null`, composer rules disable send, and `hydrationBlocksParentEvent` in `message-events.ts` blocks a parent message event when the lock is `"*"` **or equal to that event's own session id**. A non-matching id falls through to the ordinary apply path, where it is either the foreground session, a worker child, or buffered as one. Hydrate is mid-flight for exactly the session the lock names, so a live SSE row for it would land beside a transcript that is about to be replaced wholesale. Blocked events are **buffered, not dropped**: `bufferHydrationMessageEvent` holds them, `completeChatHydrationAndReplay` replays them through the same apply path the moment the lock clears, and `replayForegroundBufferedMessages` does the same for a session arriving in the foreground. Nothing that arrived during a switch is lost; it is only reordered behind the hydrate that gives it a transcript to land in.

**Cross-project clear must drop `currentSession`:** leaving the outbound session bound after `clearChat("*")` lets `deriveStageScope` stick on `phase=switching`, so the workspace never opens, if hydrate unlocks without rebinding.

**Session chat LRU stores plain clones:** `rememberSessionChatFromStore` / `captureSessionChatFromStore` unwrap + `structuredClone` store nodes before caching; restoring a Solid store proxy from the LRU would rebind an unusable session. `resumeChatSession` drops cache rows that fail to rebind `currentSession.id` and falls through to network hydrate.

**Two caches, two caps.** `SESSION_CHAT_CACHE_MAX` is **8**: whole transcripts, held in memory as plain clones, sized for the handful of chats a person moves between. `SESSION_CHROME_HYDRATION_CAP` is **128**: small per-session chrome projections, sized so revisiting a project does not re-fetch every row's chrome. The second number is not the transcript budget.

**Session chrome hydration is bounded:** duplicate requests join per store, session, and view epoch. An entry becomes hydrated only when every projection refresh succeeds. Its LRU may repeat reads after eviction or a partial failure; it cannot preserve incomplete chrome as complete.

**Not a navigation substitute:** the lock covers hydrate only; it does not replace `SwitchKind` or `clearChatForSessionSwitch` on cross-project moves.

**The switch is not the only writer.** `reconcileActiveScope` also calls `beginSessionResumeSwitch` when it resumes a visible session and the lock is not already its own, so a reconnect can take the lock, not merely respect one. It refuses to take a `"*"` lock it did not set, or a session lock naming a different session, and unlocks only while it still holds it. That keeps a reconnect landing mid-switch from unlocking a hydrate it does not own.

---

## Session prefetch

The first switch after launch is the one with nothing cached, so boot warms the chat LRU before the user asks. `prefetchRecentSessionCaches` runs once from `app-boot.ts`, after connect, off the foreground path.

It warms the **first recents row of each of the most recent `PREFETCH_RECENT_PROJECTS` (4) distinct projects**: one session per project, not the top four sessions. Recents cluster by project, so a session-ordered prefetch would spend the whole budget inside one project and leave every cross-project switch cold, and cross-project is the expensive branch that clears to `"*"` and hydrates from scratch.

Constraints that keep it a prefetch rather than a second navigation path:

- It writes only the LRU, through `warmSessionChatCache`, and touches no foreground store.
- It skips a scope already cached, so it never evicts warm entries to re-add them.
- Failures are swallowed: a prefetch that fails costs a network hydrate later, and one that reports costs the user an error about something they did not do.
- Four targets against a cache of eight leaves room for the sessions the user opens.

---

## runCreateSession ordering

New session path (`session-switch.ts`):

1. `generation.next`
2. Offline guard
3. **`clearChatForSessionSwitch("*")`**: blank transcript before any foreground
4. `openSession({ projectId, sessionId: SESSION_CREATE_PENDING_ID })`
5. `await create` on server; caller binds `currentSession` from the empty response only while `shouldApply()` is true
6. `openSession(scope)` with real id
7. `await prepareProject?`: subscribe project events so `preparing → idle` can land **and** so the first prompt's message SSE is on a live stream
8. **`completeChatSessionHydration`**: unlock; ChatView may mount immediately
9. The caller admits the first prompt immediately; its pending-send row exists before the host becomes promptable
10. Deferred **`enrich`** → `enrichCreatedSession` (bootstrap transcript merge, chrome, checkpoints; **no** `resumeProjectEventsAfter`), then `afterEnrich` (board). `onOpened` (recents) is independent

Create never uses `beginSessionResumeSwitch`. Create never awaits enrich before unlock or before admitting the first prompt; a Home idea submit must not wait on board/git. Enrichment holds the workspace *reveal* ([Den](den.md#opening-a-workspace)); `afterEnrich` stays outside that hold. Bootstrap merge semantics preserve a pending prompt that arrives before enrichment completes.

For a Home idea submit, Shell buffers both `openSession` calls while the draft remains visible. It binds the create response in the app store, subscribes project events, unlocks hydration, then commits only the real scope. The pending sentinel is an internal create fence and never becomes a painted `StageScope`. Because `sendChatPrompt` installs its pending-send row synchronously before waiting for `preparing → idle`, the first chat paint includes the submitted intent. If materialization fails, Shell returns to Home without clearing the draft.

**Create enrich must not restart SSE.** `resumeChatSession` calls `resumeProjectEventsAfter(bootstrap.event_cursor)`, which aborts the live stream. On create, `prepareProject` already subscribed; restarting from an early bootstrap cursor races the first prompt and can leave the client transcript stuck on the internal `workflow_boundary` (empty chat). `enrichCreatedSession` installs the bootstrap page with merge semantics and leaves the live stream alone.

Prompts still wait for host `preparing → idle` (`waitForSessionPromptable` inside `sendChatPrompt`); the composer stays closed while `sessionStatus === "preparing"`. After a prompt, `sendChatPrompt` re-persists the session chat snapshot so create-enrich's early boundary snapshot is not the durable SSOT.

`sendChatPrompt` seats the prompt and releases the composer draft before any wait. Its `prepareSend` step (reconnecting when offline, subscribing project events, flushing dirty editor documents) runs behind the seat, then the promptable wait, then the host send. A failure anywhere in that chain retracts the seat, reports against the chat, and hands the draft back. The send settles on the host's acceptance; the recents write and the follow-up status read trail it.

---

## runResumeSession ordering

Resume path branches on **`SwitchKind`**:

### Same-project (`SwitchKind.same-project`)

1. `generation.next` · `snapshotSessionChat`
2. **`restoreCachedSessionChat`**: bind an LRU snapshot so the store names the incoming session
3. **`beginSessionResumeSwitch(sessionId)`**: set the hydration lock and epoch; companion slices stay until hydrate replaces them
4. `openSession(scope)`
5. `await prepareProject?`: subscribe SSE if needed; skip board/git fetch when the project stream is already live
6. `await hydrate({ shouldApply, signal })`
7. `completeChatSessionHydration` · deferred `afterHydrate?` (both gated on `shouldApply`)

The selection moves before project preparation on both branches. While the event stream reconnects, preparation can take a while; the sidebar highlight and the next Cmd+] already read the new chat, and a failed preparation takes the same error path as a failed hydrate.

Same-project chat switch remounts Chat once the incoming session is bound. Context (Files, Review, Cost, …) stays mounted. `holdPresentedChat` keeps the outgoing ChatView up until `currentSession` and the transcript window name the incoming session.

Before the outgoing view releases its mounted state, its transcript viewport controller persists a bounded semantic anchor and human disclosure state. The incoming controller restores by stable row identity after its virtual window registers. A pending→real id swap transfers the mounted view in place; it does not jump to the tail or discard disclosure intent.

### Cross-project (`SwitchKind.cross-project`)

1. `generation.next` · `snapshotSessionChat`
2. **`clearChatForSessionSwitch("*")`**: must run **before** hydrate
3. `openSession(scope)`
4. **`await prepareProject?` + `subscribeProjectEvents`** after clear
5. `await hydrate({ shouldApply })`: **no** `beginSessionResumeSwitch` on this branch
6. `completeChatSessionHydration` · deferred sync (gated on `shouldApply`)

**Invariant INV-NAV-05:** the cross-project path must call neither `beginSessionResumeSwitch` **nor** `restoreCachedSessionChat`. The branch has already cleared to `"*"` and dropped `currentSession`, and either call would rebind a session behind that clear: the resume lock by narrowing the wildcard, the cache restore by putting a session back. The same audit requires `restoreCachedSessionChat` on the same-project branch, so the two are checked as a pair.

### Error path

Resume, on hydrate failure: `onSessionNotFound?` (awaited **first**, and only for a session-not-found error; its own throw is swallowed so the reset still runs) · `resetChat` · `returnToHome` · `reportError`.

Cleanup leads because it is the only step that needs the failed scope while the store still names it; the reset that follows is unconditional. Create has no `onSessionNotFound` branch and runs `resetChat` · `returnToHome` · `reportError` with `session_create_failed` against the app scope rather than a session scope.

Both paths re-check `isLatest` before touching anything: a switch that lost the race must not drag the user Home out from under the switch that won.

---

## openProject flow

`components/shell/session-navigation.ts`:

```text
openProject(projectId):
 kind = stageSwitchKind(shell, projectId)
        // shell.state.activeProjectId absent or equal → same-project, else cross-project
 enterProject(projectId, kind, { stageSwitchDone: true }):
   shell.beginStageSwitch({ projectId, kind })
   row = recents.find(r => r.projectId === projectId)   // first local match
   if row → resumeSession(row, { kind, stageSwitchDone: true }) → runResumeSession
   else   → startEmptySession({ kind: "new-session", projectId })
```

**Capture `SwitchKind` before `beginStageSwitch`.** The kind is read from `shell.state.activeProjectId` before the begin, then handed down explicitly; `stageSwitchDone: true` tells `resumeSession` the stage switch has already begun so it neither recomputes the kind nor begins it twice. Recomputing after begin sees `activeProjectId === target` and answers `same-project` for a move that is not, which skips `clearChat("*")` and leaves the outgoing project's transcript on screen.

`recents-store` order is **local MRU** (`RECENTS_CAP` enforced in store). Welcome **display** order remains server `last_opened_at`; see [`projects.md`](projects.md).

---

## reconcileActiveScope

**Implementation:** `session-reconcile.ts`, called from `app-connection.ts` on reconnect / SSE open, and from `resumeChatSession` when restoring a cached snapshot.

| Property | Rule |
|----------|------|
| Purpose | Refresh workflow + messages for a foreground the client already believes it has: after a network gap, or after a resume served from the chat cache |
| Not for | Driving navigation. It never decides `SwitchKind`, clears chat, or commits stage scope; `runResumeSession` / `runCreateSession` own those. A cache-hit resume calls it *inside* a switch with `fromSessionSwitch: true` |
| Ordering | Workflow refresh before applying messages (reconnect stale-run guard) |
| Stale apply | Default `shouldApply` is "`currentSession.id` still matches the reconcile target"; drop writes when false |
| Lock rule | Reconnect must **not** steal `chatHydrationLock === "*"` or a different session-scoped lock. A switch-held hydrate sets `fromSessionSwitch: true` |
| Unlock | `completeChatHydrationAndReplay` / error unlock only when this reconcile still holds the lock |

---

## Shell wiring

`Shell.tsx` must:

- Delegate resume/create to `runResumeSession` / `runCreateSession` with `shellSessionSwitchGeneration`
- Pass the `SessionSwitchDeps` mapping to `appStore` hydrate actions + `shell.openSession`
- Thread hydrate `{ shouldApply }` into `resumeChatSession`
- Use **`beginStageSwitch`** / **`clearToHome`**, not ad-hoc project activation from Shell; Home first-submit is the one buffered commit path
- **Veil before minting.** A create that has no project id yet (new project, open folder, clone) calls **`beginWorkspaceOpening`** before its request and hands off with `beginStageSwitch` in the same batch as the registry upsert; never `clearToHome` on the way out of a project ([Project space](project-space.md#active-project-state))
- Skip no-op re-select only when foreground, `currentSession.id`, and unlocked hydration already agree (stuck `phase=switching` must re-hydrate)

Draft materialization calls `createSessionForProject` through `runCreateSession`. Shell controls stage scope and generation fencing; the lifecycle function returns session data without switching shell scope.

---

## Test matrix

| Layer | Path | Covers |
|-------|------|--------|
| Unit | `chat/session/session-switch.test.ts` | Generation; create unlock-before-enrich; resume open-before-hydrate; stale/offline |
| Unit | `chat/session/session-lifecycle-create.test.ts` | Create bind, enrichCreatedSession (no SSE restart), preparing→idle prompt gate |
| Unit | `chat/session/session-reconcile.test.ts` | Reconnect lock rules |
| Unit | `shell/stage-scope.test.ts` | `deriveStageScope` phase machine |
| Integration | `chat/session/session-transcript-hydrate.integration.test.tsx` | Shell → session-switch hydrate |
| Audit | `shell/den-client-state-invariants.test.ts` | SwitchKind / clearChat structural guards |
| MSW | `chat/session/session-resume.integration.test.ts` | Stale switch races |
| E2E | `e2e/session-switch.spec.ts` | Composer visible after launcher switch |
| E2E | `e2e/project-switch.spec.ts` | Cross-project no stale transcript |
