# Den context, action menus, and reveal

Den has one accessible in-window menu primitive for both pointer context menus and explicit overflow controls. A menu is offered where it makes an action more discoverable, preserves a grounded source reference, or completes a local editing workflow. It is not a substitute for a primary navigation control.

**See also:** [Den](den.md) · [Source navigation](source-navigation.md) · [In-view find](den-in-view-find.md) · [Keyboard shortcuts](keyboard-shortcuts.md)

## Boundaries

| Plane | Purpose | Boundary document |
|---|---|---|
| **Context and action menus** | Local actions, path transfer, and reveal | This page |
| **Source navigation** | Open a source in Den or an external editor; confirm external URLs | [source-navigation.md](source-navigation.md) |
| **In-view find** | Find within the focused view | [den-in-view-find.md](den-in-view-find.md) |
| **Native application menu** | The OS menu bar, a projection of the command catalog | [keyboard-shortcuts.md](keyboard-shortcuts.md) |

Reveal does not open a file editor. Opening source does not reveal it in the OS file manager. In-view find does not turn into global search.

The native menu bar is the one menu surface that is not `ContextMenu.tsx`. [`app-menu-model.ts`](../lycaon-den/src/shortcuts/app-menu-model.ts) builds it from `app_menu.*` menu contributions and hands a spec to [`app-menu.ts`](../lycaon-den/src/platform/desktop/app-menu.ts), which pushes it to the OS; the OS draws it, so it has no roving focus, no submenu keyboard model, and no styling of ours. Its rows are commands from the same catalog with the same relevance and enablement, and its accelerators come from the same resolved keymap, so an app-menu item and a context-menu item for one command cannot drift in availability. A placement may carry its own label, which is the one way their labels can differ.

## Menu system

- [`ContextMenu.tsx`](../lycaon-den/src/components/ContextMenu.tsx) is the sole in-window menu primitive. It supports commands, disabled commands, destructive commands, separators, headers, checkbox items, and one level of submenus (a deeper submenu throws). Keyboard navigation skips headers, separators, and disabled rows. A separator is inserted before the first destructive row.
- The same component opens from right-click, keyboard context-menu activation, or a visible overflow control. The trigger determines the interaction; it does not create a second menu system.
- Every custom menu prevents the webview-native menu. [`TextEditContextMenuHost`](../lycaon-den/src/components/TextEditContextMenuHost.tsx) installs the production native-menu guard after its text-edit listener; development leaves the native menu available for inspection.
- Menus use roving focus and dismiss on Escape, outside pointer input, scroll, or window blur; a resize re-places the menu and closes it only when an element anchor leaves the viewport. Right Arrow or Enter enters a submenu; Left Arrow or Escape returns to its parent item.
- Message action toolbars reserve their own space in the message layout. Hover and keyboard focus reveal their controls without covering links or shifting prose.
- A right-click over a focused text field leaves DOM focus in the field, so its selection stays painted, and navigates the menu virtually from the document; any key the menu does not use returns to the field and dismisses. Keyboard invocation moves real focus into the menu instead, and menus that take focus hand it back with the selection when dismissal would otherwise leave focus nowhere. A menu takes focus once its surface is placed, so opening lays the document out once for placement and once for focus.
- An authored top level **targets** at most twelve action rows and at most three groups. This is a review target, not a runtime limit: `ContextMenu.tsx` counts nothing and truncates nothing. Closely related command families move into a single submenu rather than lengthening the root. Dynamic user or host inventories may exceed the target.
- Menus can straddle their anchor to use the available viewport before scrolling. Every menu is an OverlayScrollbars scrollport, so one that still overflows scrolls; overflow is a supported fallback, not the authored layout.
- Shared path actions use the same order everywhere: Open, Reveal in tree, Open in, Copy path, Copy relative path, then Add to chat, omitting unavailable actions. Surface-specific actions retain their local grouping; destructive actions remain at the end. Copy variants stay adjacent and directly accessible. Open actions require an opener; Reveal in tree has its own command.
- Shortcut hints occupy a separate trailing column; action labels remain unchanged when a key binding changes. Submenu arrows align at the trailing edge.
- Window actions share Open in new window (or Open another window) → Focus window → Close window, omitting actions the surface does not support.
- Chat lists share Export → Archive chat (or Unarchive chat) → Delete chat…, including the same destructive styling and separator.
- Platform labels come from `localPathDestinationLabel("file-manager")`; chrome never hard-codes Finder.

Every menu in the inventory below is authored in Den. The host also defines a closed slot vocabulary for contributed menu placements (`editor.context.analysis`, `editor.context.edit`, `editor.toolbar`, `file.context`, `project.context`, `session.context`, `composer.actions`, alongside the `app_menu.*` bars) in [`vocabulary.go`](../lycaon/internal/contribution/vocabulary.go). The host validates, merges, and serves contributions into those slots, and an example pack exercises `editor.context.analysis`; Den consumes the `app_menu.*` slots and `editor.context.analysis` / `editor.context.edit`. A placement only renders where a surface requests its slot. Wiring one is a Den change, not a vocabulary change.

## Shared action ownership

[`context-action-catalog.ts`](../lycaon-den/src/components/context-action-catalog.ts) owns built-in context action identities, labels, destructive presentation, and keyboard-command bindings. [`context-actions.ts`](../lycaon-den/src/components/context-actions.ts) binds those definitions to captured targets, prevents disabled dispatch, and reports failures. Command-backed actions resolve their live label, enablement, and shortcut through the contribution registry. Contributed actions and host-authored secret actions retain their own catalog owners and use the same dispatch boundary.

Domain helpers own composition and target-specific capabilities: path actions, copy variants, text editing, chat lifecycle and export, and window actions. Surfaces supply facts and callbacks; they do not redefine shared action labels or fork launch behavior. A new built-in action gets one catalog identity and one owning implementation; a dynamic target label belongs to its domain helper.

### Open destinations

[`open-in-menu-items.ts`](../lycaon-den/src/components/open-in-menu-items.ts) provides one **Open in** submenu for context and overflow menus, and explicit **Open in…** buttons use the same destination definitions. All path entry points use [`open-local-path.ts`](../lycaon-den/src/platform/navigation/open-local-path.ts) to resolve availability, preferences, and launch behavior; the action binding reports failures. Navigation inside Den (open a source, reveal in tree) is [`open-source.ts`](../lycaon-den/src/platform/navigation/open-source.ts), the [source navigation](source-navigation.md) contract.

- Files offer the platform file manager, configured editor, browser for the formats in the shared file-format catalog (`lycaon-den/shared/local-file-formats.json`), and default application, in that order. Folders offer the file manager and configured editor; the file manager reveals the item, and editors receive folder launch arguments without file line suffixes.
- Browser and editor labels name the configured application when known.
- External opening uses the current file on disk. It does not save a draft or export historical content. Source line numbers are retained for editor opening.
- Deleted files and unresolved worker locations retain disabled destinations with reasons. Host paths on another device omit local destinations. Browser-only clients explain that local opening requires the desktop app.
- Device exports retain their explicit saved path as their allowed root even when connected to a remote host. Browser downloads without an absolute path show download guidance instead.
- Web links offer the same **Open in** submenu (the browser, or Default application for mail and telephone links) and Copy link; external-link confirmation remains in force.
- Reveal in tree, copy actions, and window actions remain separate from Open in.

## Inventory

This inventory is the shipped product boundary. Update it in the same change as a menu-surface change.

### Project and chat navigation

| Surface | Trigger | Commands |
|---|---|---|
| Chat rows in the focused list | Context menu | Open, window/view actions when applicable, Rename, Pin or Unpin, Move up and Move down on pinned chats, Copy title, Copy session ID, export, Archive chat, Delete chat… |
| Chat rows in All chats | Context menu | Open, Pin or Unpin on the active tab, export, Archive chat on the active tab or Unarchive chat on the archived one, Delete chat… |
| Project navigation context | Context menu | Project-local navigation actions, including opening an existing detached view in its window |
| Attached-folder compact row and folder panel row | Context menu | Rename in the folder panel, Reveal in tree, Open in, Copy path, Copy relative path when applicable, Add to chat, Remove folder from project |
| Attached-folder compact row | Visible row control | Remove folder from project |

Archiving is one-way from the focused list: it is a way to put a chat down, and the way back is All chats, the surface that can show an archived chat. Both lists use the same labels and relative order for their shared actions.

Removing a folder removes it from the project; it does not delete it from disk. The folders panel confirms the visible removal. A root with dependent project state may require the existing project-level confirmation before removal completes.

### Paths and sources

| Surface | Trigger | Commands |
|---|---|---|
| Resolved project paths in source chrome, markdown, tool facts, git rows, citation paths, and user attachment chips | Context menu | Open, Reveal in tree for primary workspace paths, Open in, Copy path, Copy relative path when applicable, Add to chat |
| Diff viewer header | Copy control and More overflow | Copy path / relative path / file name; Reveal in tree, Open in, Add to chat when the diff resolves to an attached file |
| Files tree background | Context menu | New file, New folder, Open in for the root, Collapse all, Expand all |
| Files tree row | Context menu | Folder creation and expansion for folders; Rename except the root; Duplicate; Move to…; the shared path actions (Open and pop out where relevant, Open in, Copy path, Copy relative path except the root, Add to chat); Restore comparison baseline for a deleted file; Explain in review where applicable; Move to trash except the root |
| File tabs and tab strip | Context menu | File lifecycle, pinning, path copy, reveal, Add to chat, and window actions appropriate to the tab or strip |
| Files editor, breadcrumb, gutter, changed-file row, image viewer, info card, and review hunk | Context menu | The source-specific editing, review, navigation, copy, reveal, and attachment actions implemented by [`project-files-context-menu.ts`](../lycaon-den/src/files/commands/project-files-context-menu.ts). Editor selection verbs use a one-level submenu; path-copy commands stay directly accessible |
| Info viewers, folder summaries, and saved-report or trash-error actions | Open in button | The shared destination list for the captured filesystem target |
| Web links | Context menu | Open in (browser, or Default application for mail and telephone links), Copy link |
| Files editor Find and Go to line controls | Visible overflow control | Their complete focused-file finding and navigation command sets |

Host-authored path references offer Open and Reveal in tree while resolution is pending or ambiguous. Both commands retain their action through the shared lookup and candidate chooser. A missing or unavailable result remains an explicit outcome; it does not acquire a guessed path for copy, attachment, or file-manager actions. Known worker references omit primary-tree reveal.

The Files code editor is a source-aware editing surface. Its context menu may offer source selection verbs and a grounded file attachment even where a generic text field would offer only selected text.

**Secret spans** add one block to the editor menu, after the edit verbs and symbol actions, once the buffer has a host document identity. A caret inside a screened span leads with a header naming the evidence, then **Track as secret…** (untracked spans only), **Copy reference** (tracked spans only), **Find other uses in this file** with the count when the value repeats, **Ignore this value in this project…** (untracked detections only), and **Open in Secrets**. With no span under the caret, an eligible selection gets **Mark as secret…** alone, since the case where no rule matched is the one the action exists for. The block fits the twelve-row target without a submenu; see [files-stage.md § Secret spans](files-stage.md#secret-spans).

### Transcript, artifacts, and text

| Surface | Trigger | Commands |
|---|---|---|
| Generic editable field (`input`, `textarea`, `contenteditable`) | Context menu | Cut, Copy, Paste, Select all; Add to chat when a selection exists |
| Chat composer | Context menu | **Mark as secret** when a selection exists, then Cut, Copy, Paste, Select all. The composer is its own add-to-chat destination, so it offers no Add to chat |
| Live selection in the transcript selectable island | Context menu | Copy, Select all, Add to chat; Cut and Paste remain visible but disabled |
| Worker card | Context menu | Open worker, Copy job ID, Open evidence when present, and grounded touched-path attachments |
| Tool spill path | Context menu | Copy path and Add to chat as selected text with spill-path provenance; never Reveal or a project file reference |
| Expanded structured-tool raw-output chrome | Visible links | Raw output in Files opens the complete recorded output; Find related tool calls searches other calls to the same tool in this chat; Add to chat only as a `search-hit` when project ID, chat ID, and tool-call ID are all present |
| Collapsed tool chicklet | — | No raw-output attachment action |
| Project artifact tiles | Context menu | Reference-impact header, Add to chat where supported, and confirmed Delete artifact…; deletion leaves historical references visible and marked deleted |
| Transcript artifact tiles | Context menu | Add to chat |

Text selection uses [`textEditMenuItems`](../lycaon-den/src/components/text-edit-menu-items.ts) and [`TextEditContextMenuHost`](../lycaon-den/src/components/TextEditContextMenuHost.tsx). The generic selection attachment has a fixed `[User selected text: …]` provenance header. A stamped, resolvable project path may additionally yield a grounded `path-file` reference.

Right-clicking a source link opens its path actions even when text is selected. The selection menu handles ordinary prose and nested chrome; it is also the fallback when a source target has no available path menu.

### No menu

Home tiles, empty states, non-field settings chrome, arbitrary transcript prose without a live selectable-island selection, network host rows, and collapsed tool chicklet headers do not grow a context menu. A tab, navigation item, or visible overflow control may have a menu only for actions the surface already provides; menus do not hide the primary navigation affordance.

## Add to chat

Every attachment action uses the same destination boundary.

- Chat surfaces add to the chat they belong to, not to the session a row came from. A chat's transcript adds to that chat; a worker's drawer (task, activity, evidence) adds to the chat that dispatched the worker, while its references keep citing the worker's own session and tool call. The owner declares this once for the whole surface ([`chat-destination-scope.tsx`](../lycaon-den/src/chat/composer/chat-destination-scope.tsx)); a surface whose chat is unknown asks, as project surfaces do.
- Project and global source surfaces invoke **Choose a chat**, with the visible chat merely suggested. A project path, artifact, or grounded search hit restricts the chooser to its project. The chooser stages an attachment; it does not send a message or change the active stage.
- Raw tool output never degrades to copied or selected output text. It is available only as a `search-hit` with `source_ref = tool_call_id` and `hit_kind = tool`.
- A diff attaches only after Shell resolves its absolute path to an attached root ID. Unresolved in-hand diffs remain viewable and copyable, but are not attachable as files.

## Desktop drops and attachment reads

The desktop main window keeps Tauri `dragDropEnabled` on. Native drag/drop supplies the paths and coordinates that project path references need; the web/headless path falls back to `File[]` through [`file-drop.ts`](../lycaon-den/src/platform/files/file-drop.ts).

For an in-project byte attachment, `path_kind` and `read_path_bytes` apply the same conservative Unix symlink policy: no component after an attached root may be a symbolic link. This prevents a drop from being accepted as an image and later failing during the descriptor-walk byte read. External snapshot imports use their own `import_path_kind` classification.

## Reveal in file manager

```ts
revealInFileManager(
  absolutePath: string,
  projectRoots: readonly string[],
  options?: RevealInFileManagerOptions,
): Promise<RevealInFileManagerResult>
// revealed | rejected
```

| Platform | Native action | User-facing label |
|---|---|---|
| macOS | `open -R <absolute path>` | Finder |
| Windows | `explorer /select,<absolute path>` | File Explorer |
| Linux | `org.freedesktop.FileManager1.ShowItems(file:// URI)`; fall back to `xdg-open <parent>` | Files |

The Linux URI percent-encodes reserved and UTF-8 bytes. Browser-only clients disable local destinations with a desktop-app explanation; copy remains a separate action.

Both Den and the Tauri command validate that the absolute path is equal to or under one of the roots supplied for the action. This is a component-aware product guard against accidental out-of-project actions, repeated at the native spawn point. Root values originate in the client's current project state; this renderer-to-Tauri command boundary is not an authorization boundary against a compromised renderer. A second command, `reveal_typed_path_in_file_manager`, deliberately skips the root check for a path the person typed into Crossbar. Components never invoke `open`, `explorer`, `xdg-open`, or D-Bus directly.

## Related implementation

- [`context-menu.ts`](../lycaon-den/src/platform/interaction/context-menu.ts) · [`ContextMenu.tsx`](../lycaon-den/src/components/ContextMenu.tsx) · [`TextEditContextMenuHost.tsx`](../lycaon-den/src/components/TextEditContextMenuHost.tsx) · [`selection-lease.ts`](../lycaon-den/src/platform/interaction/selection-lease.ts)
- [`text-edit-menu-items.ts`](../lycaon-den/src/components/text-edit-menu-items.ts) · [`selection-provenance.ts`](../lycaon-den/src/chat/composer/selection-provenance.ts) · [`add-to-chat.ts`](../lycaon-den/src/chat/composer/add-to-chat.ts)
- [`reveal-in-file-manager.ts`](../lycaon-den/src/platform/files/reveal-in-file-manager.ts) · [`reveal_in_file_manager.rs`](../lycaon-den/src-tauri/src/reveal_in_file_manager.rs) · [`read_path_bytes.rs`](../lycaon-den/src-tauri/src/read_path_bytes.rs)
- [`project-path-menu-items.ts`](../lycaon-den/src/components/project-path-menu-items.ts) · [`project-files-context-menu.ts`](../lycaon-den/src/files/commands/project-files-context-menu.ts)
- Native menu bar: [`app-menu-model.ts`](../lycaon-den/src/shortcuts/app-menu-model.ts) · [`app-menu.ts`](../lycaon-den/src/platform/desktop/app-menu.ts) · slot vocabulary in [`vocabulary.go`](../lycaon/internal/contribution/vocabulary.go)
