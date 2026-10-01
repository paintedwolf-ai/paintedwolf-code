# Keyboard shortcuts

Keyboard shortcuts are one projection of the command catalog. The command defines behavior; a keybinding only says when a platform chord or leader sequence may invoke it.

**See also:** [Den](den.md) · [Search](search.md) · [Extend](extend.md#contribution-language) · [Accessibility](accessibility.md)

**Machine truth:** stock declarations under [`lycaon/config/packs/painted-wolf/platform/contributions/`](../lycaon/config/packs/painted-wolf/platform/contributions) (`commands/`, `keybindings/`, `menus/`) · `./task codegen:contribution-inventory` → `lycaon-den/src/contributions/stock-frame.generated.ts` and `native-ui-handlers.generated.ts` · [`shortcuts/keymap.ts`](../lycaon-den/src/shortcuts/keymap.ts) (resolution and conflict classes) · [`shortcuts/binding-policy.ts`](../lycaon-den/src/shortcuts/binding-policy.ts) + [`shortcuts/platform.ts`](../lycaon-den/src/shortcuts/platform.ts) + `shortcuts/reserved-chords.generated.ts` (refusals) · [`shortcuts/dispatcher.ts`](../lycaon-den/src/shortcuts/dispatcher.ts)

This separation prevents three drifts: a shortcut running different behavior from its menu item, help text describing a chord that is no longer bound, and client code inventing commands that the host cannot validate.

## Command, placement, and binding

```text
command = identity + title + relevance + enablement + typed action
menu    = command + placement
binding = command + (platform chord | leader sequence) + dispatch scope
crossbar = command unless palette:false
```

A stock or extension command may have any combination of Crossbar, menu, keybinding, and host-panel reachability. At least one route must exist, or compilation rejects the unreachable declaration.

Commands are offered in Crossbar by default. `palette: false` is the opt-out for commands that make sense only in a menu, chord, or host-rendered panel. Menu placement does not hide a command from Crossbar.

The host contribution frame validates references, collisions, conditions, and platform defaults before Den installs a handler. Den then routes an accepted chord to the current command rather than embedding action logic in a global keydown switch, so every entry point shares the command's identity, conditions, input schema, authorization path, and result. Help, menus, Settings, and Crossbar project the current effective frame instead of maintaining copies.

## Dispatch scopes

Bindings declare where they compete:

| Scope | Intended reach |
|---|---|
| Global | Application navigation and commands that are safe from any surface. |
| Files | Source tree, buffer, editor, and review actions. |
| Composer | Prompt and chat composition actions. |
| Overlays | The currently open modal, picker, or command surface. |

The focused surface gets the first opportunity within its scope. Overlay bindings do not leak to the page below. Commands that permit use while a text input is focused must opt in (`allow_in_input`), so ordinary typing is not intercepted by global dispatch.

Escape is handled as dismissal by the active overlay or surface before it can become a general command. A layer that closes itself on an Escape press owns that press, so one Escape never closes a layer and then falls through to the next. The dismissal chain has a floor for the Files stage: when no overlay, menu, find bar, or other layer is open, Escape first closes an open walk, leaving its selected version in place, and otherwise, when the editor shows a past version, runs **Return to current version** (`files.currentVersion`), gated by the `files_version_historical` fact rather than by any Escape binding of its own (`components/shell/shell-dismiss-command.ts`). Tab and arrow-key behavior inside widgets is part of the widget's accessibility contract, not the command registry; the Files stage installs no window-wide key handler of its own.

Files has two independent undo domains. An editor or text field keeps its document undo; the collaborative text editor uses CodeMirror history mapped through remote changes, and keyboard Undo/Redo includes explicit human document actions and restores all selection ranges. With focus elsewhere in Files, the host-owned lifecycle commands `files-undo-lifecycle` and `files-redo-lifecycle` undo and redo create, rename, move, duplicate, and Trash operations. Their defaults are not symmetric across platforms (redo carries a second `Mod+Y` chord on Windows and Linux), so the current pair is read from **Settings → Keyboard shortcuts**, not from this page. The persistent Files header buttons expose the same commands and their exact current action labels.

The Files gutter has one Tab entry. On a line-facts handle, Up and Down move between visible fact lines, Right or Enter enters the card, and Left or Escape returns to the line. Within the card, Up and Down move between rows; Tab can leave it. Fold, finding, change-bar, and restore controls have their own activation keys.

## Platform bindings

Bindings declare separate macOS, Windows, and Linux defaults because modifier conventions and reserved operating-system chords differ. The contribution frame exposes the resolved default for the running platform.

Documentation does not repeat the chord table. The effective catalog and **Settings → Keyboard shortcuts** are authoritative because extensions, conditions, and user remapping change what is active. When UI copy needs to show a chord, it asks the shortcut registry for the current display binding; it never hardcodes `Mod+…` text beside a command.

## Editing styles and editor commands

Editor settings default to nonmodal editing with familiar Emacs movement, mark, kill-ring, yank, and Control-X sequences; the default uses the standard caret and adds no mode label or panel. Optional Vim editing supports operators, counts, text objects, visual selection, registers, repeat, search, and Ex commands, with its Normal, Insert, and Visual state and transient command entry in the editor status bar. Neither style changes the document owner: save, close, undo, and redo use the same operations as app commands.

Explicit shortcut overrides take precedence over the editing style, which then handles its familiar keys before stock editor bindings. Editor operations such as moving and copying lines, commenting, indentation, folding, completion, matching brackets, and expanding selection are also catalog commands, available through Crossbar and shortcut customization. The inline-edit binding takes precedence over CodeMirror's fallback syntax-selection binding.

Tab normally indents. Editor settings can make it move focus instead; Escape followed by Tab provides a temporary exit. Next region and Previous region move between app regions, including the file tree, tabs, editor, chat, and composer, and remain available while editing. Entering a region lands on its first target by priority: the current chat row in the sidebar, the editor or file tree in a stage (never its title-bar controls or a hidden surface), and the composer in chat. Clicking into the transcript leaves focus there, so Page Up, Page Down, and Space scroll it. File tabs can be reordered by command within their pinned or unpinned group. Typing a filename prefix in the tree searches its expanded rows, including virtualized rows.

Chat message navigation follows logical messages across virtual windows and older transcript pages. Arrow keys on a focused message move between messages; Escape returns to the composer. Message actions have one Tab stop and use Left/Right to select an action. Queue editing and removal restore focus to a surviving queue control or the composer.

The status bar reserves space for cursor position, file format, unsaved changes, modal state, and exceptional conditions. Normal editing and brief preserving or synchronizing transitions stay silent. Wrap stays in the status bar with the same neutral text color in both states; its pressed state remains accessible. Word wrap and line-number controls are also available in the View menu, Crossbar, and Settings.

## Leader sequences

Modified single chords are a scarce, contested resource, and the largest family competing for them (go to chat, go to files, go to each project context) is also the one where a mnemonic matters most. A **sequence** spends one chord to buy a namespace: a leader chord, then one bare key.

A declaration spells the second step relative to the leader (`bindings: ["Leader C"]`), never as a literal prefix, because the leader is itself a rebindable entry: `nav.leader`, defaulting to `Mod+;`, with its own **Leader key** row in Settings. Roughly a fifth of the stock keybindings are declared this way. Resolution substitutes the current leader into every such declaration, so rebinding the leader moves the whole namespace at once and rewrites any sequence overrides hanging off the old one.

What the dispatcher enforces:

- Only the leader opens a sequence, and only outside overlay scope. An overlay keeps Escape and its own chords.
- The second step is a **bare** key: no Command, Control, or Alt, and no shifted letter. A modified key does not resolve.
- An armed leader owns the next key. That key never reaches an editable control, whether or not it matches a binding; Escape cancels, and pressing the leader again restarts the capture.
- A pending leader expires after 1.5 s and paints nothing while armed, so a sequence typed from memory never flashes UI. Sequences are discovered where every binding is: Crossbar, the keyboard shortcuts help, and Settings.
- The leader cannot equal an active catalog chord, a user binding cannot claim the leader's chord, and a sequence override must start with the current leader. Settings refuses each with the reason.

## Refused bindings

A chord can be refused outright, before any question of collision. `bindingPolicyFailure` answers with one of three:

| Refusal | Meaning |
|---|---|
| `syntax` | Not a parseable chord or sequence |
| `reserved` | Claimed by the operating system (`os`), by the platform's app conventions (`system`), or by assistive technology (`assistive`) |
| `unproducible` | This platform's keyboard cannot produce it |

`reserved` splits three ways. **OS chords** are consumed by the window server before any app sees them: Spotlight, the app switcher, the Mission Control arrows, the Dock toggle, and the screenshot family on macOS; `Alt+Tab`, the security screen, and anything holding `Super` on Windows and Linux (`reserved-chords.generated.ts`). **System chords** are the per-platform app conventions the menu bar owns, which the app does receive: quit, close, hide, minimize on macOS; `Alt+F4` on Windows and Linux (`reservedChords` in `platform.ts`). **Assistive keys** (CapsLock, Insert, ScrollLock) are reserved on every platform, because a screen-reader user navigates with them and an app that binds one takes away the way out.

The three are not enforced identically. A contributed **default** may sit on a system chord, because shipping the platform's own spelling of Close or Quit is correct; it may never sit on an OS chord or an assistive key, since the key press never arrives and such a binding would be dead on arrival and invisible in testing. A **user** binding may use none of the three. Settings refuses at the moment of capture and names which applies, so a refusal is a sentence rather than a key press that appears to do nothing.

## Collision model

Two active commands can claim the same binding in the same scope. Collision handling is deterministic and visible:

1. inactive `when` branches are removed;
2. the focused dispatch scope is selected;
3. an explicit user binding wins over a default;
4. an unresolved collision is not guessed from registration order.

Bundled declarations never collide. When two stock keybindings claim one chord in one scope on one platform, contribution compilation fails (`internal/contribution/compile.go`), because an equal-rank tie would deactivate both and ship a dead chord. Collisions between a stock default and an installed pack, or between installed packs, remain the runtime cases below.

Three collision classes exist, because there are three ways to claim the same keystroke:

| Class | Key | Detected by |
|---|---|---|
| Chord | scope + chord | `detectConflicts` |
| Sequence | scope + leader + second key | `detectSequenceConflicts` |
| Leader shadow | an ordinary chord equal to the resolved leader | `leaderShadowedBindings` |

A leader shadow is not a tie between two commands: the leader wins by construction, and the shadowed binding can never fire. It is reported separately so the fix is obvious: move the leader or move the binding.

Settings shows the competing command identities and lets the user choose or remove a binding. A conflict cannot cause both actions to run.

## Conditions

A keybinding may add a placement condition, but it cannot contradict the command's own relevance or enablement. Effective availability is the conjunction of command relevance, command enablement, placement, current scope, and binding conditions. A relevant but disabled command does not consume the chord.

Conditions use typed host/client facts such as focused surface, active stage, selection presence, or overlay state. Shortcut dispatch never classifies visible prose or DOM labels to infer intent.

## Menus and Crossbar

Menus, shortcuts, Crossbar, and host-rendered input panels are peers: each resolves to the compiled command and converges on shared command dispatch. A menu and Crossbar keep a relevant command visible with the same host-derived disabled reason. The shortcut path does not consume its chord while disabled and produces the same structured rejection if state changes between display and invocation.

## Settings and customization

Settings projects effective commands and bindings grouped by command category. Edits change binding desired state, not command definitions. Reset removes the override and reveals the current contributed default.

An override is keyed by **keybinding declaration id**, not by command id, because one command may carry several declarations with different scopes or platform defaults and a rebind has to retarget exactly one of them. `nav.leader` is a key in the same map.

Stored overrides are re-validated against the live frame and the running platform every time they load, and one that no longer holds is dropped rather than applied: an unparseable value, a policy refusal, a binding that shadows the current leader, a sequence whose leader has moved, a declaration the frame no longer carries, or a conflict introduced by a catalog change. A user's shortcut is desired state; the frame, not the stored preference, decides what is live.

## Accessibility

Every command reachable only by a chord is a design error. Consequential actions also need a visible, labeled route unless the action is intrinsically keyboard-only.

Shortcut presentation must expose command names to assistive technology, render platform-appropriate modifier names, avoid color as the only conflict signal, keep focus in the invoking surface after a failed dispatch, and respect the reduced-motion and focus-ring rules of the surrounding control.

Keys assistive technology navigates with are never bindable, by defaults or by the user (see [Refused bindings](#refused-bindings)). That is a guarantee, not a default the user can trade away.

Focus order, traps, and widget navigation are defined by [Accessibility](accessibility.md); this page defines command bindings.

### Walk history

The walk rail contributes one sequential Tab stop for its selected marker. Left or Down requests the previous step; Right or Up requests the next. Home and End request the first and latest steps. A pending comparison keeps focus and the playhead on the displayed step until the requested step settles. Escape closes the walk.

With the walk controls grip focused, arrow keys move the floating controls; Shift moves them farther. End docks above the editor status bar, and Home returns to the initial floating position. Placement is remembered across walks and restarts. Editor settings choose the default placement and reset a saved location.

The edge arrows browse the track without changing the selected file. The position counter opens **Jump in walk**, with browsable turns, prompt excerpts, and file or command changes. Search is optional. Down from the search field enters the results; Up and Down move between results, and Enter selects a turn or change. Escape dismisses this popover and returns focus to the counter without closing the walk.

## Adding a binding

1. Declare or identify the command.
2. Add a keybinding contribution with platform defaults and the narrowest dispatch scope. Navigation-family commands take the `Leader <key>` form rather than another modified chord.
3. Add menu placement when the action needs a visible route.
4. Set `palette: false` only when Crossbar would be misleading.
5. Run `./task codegen:contribution-inventory` and validate the full frame.
6. Verify dispatch from the intended focus contexts and from text input.

The exact YAML fields are documented by [Extend](extend.md#contribution-language) and the contribution schemas.
