import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { Match, Show, Switch, createMemo, createSignal, onMount } from "solid-js";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { ContextMenu } from "../../components/ContextMenu.tsx";
import { InlineRenameInput } from "../../components/inline-rename/InlineRenameInput.tsx";
import { renameEntryPath, renameSelectionRange } from "../commands/project-files-create.ts";
import { FilesScopeChangeMark } from "../components/FilesScopeChangeMark.tsx";
import { scopeChangeKindFromFlags, type ScopeChangeKind } from "../components/files-scope-change-mark.ts";
import { trailingTabState } from "../tabs/files-tab-trailing.ts";
import { FilesAgentChip } from "../components/FilesAgentChip.tsx";
import { agentNameStyle, type FilesAgentChip as FilesAgentChipValue } from "../components/files-agent-presence.ts";
import { type FlatTreeRow } from "./project-files-tree-flat.ts";
import type { DisplayRow } from "./files-tree-display-model.ts";
import { foldedTreeDepth, type StickyAncestor, type StickyFold, type StickySlot } from "./files-tree-sticky.ts";
import { useTree } from "./files-tree-context.ts";

type RowTrailing =
  | { kind: "none" }
  | { kind: "scope"; changeKind: ScopeChangeKind }
  | { kind: "presence"; chip: FilesAgentChipValue };

function sameTrailing(a: RowTrailing, b: RowTrailing): boolean {
  if (a.kind !== b.kind) return false;
  if (a.kind === "presence" && b.kind === "presence") {
    return a.chip.state === b.chip.state && a.chip.description === b.chip.description;
  }
  if (a.kind === "scope" && b.kind === "scope") {
    return a.changeKind === b.changeKind;
  }
  return true;
}

function FolderIcon(props: { open?: boolean }) {
  return (
    <ThemeIcon
      slot={props.open ? "folder-open" : "folder"}
      size={14}
      class="den-files-tree__icon"
    />
  );
}

function NewFileIcon() {
  return (
    <ThemeIcon slot="new-file" size={13} class="den-files-tree__icon" />
  );
}

function NewFolderIcon() {
  return (
    <ThemeIcon slot="new-folder" size={14} class="den-files-tree__icon" />
  );
}

function FileIcon() {
  return (
    <ThemeIcon slot="file" size={14} class="den-files-tree__icon" />
  );
}

/** Indentation uses the displayed fold before rows mount. */
function treeIndent(depth: number, fold: StickyFold, offset = 0): string {
  return `${foldedTreeDepth(depth, fold) * 14 + offset}px`;
}

function RenameRow(props: {
  paddingLeft: string;
  name: string;
  path: string;
  busy: boolean;
  onSubmit: (value: string) => Promise<boolean>;
  onCancel: () => void;
}) {
  return (
    <div
      class="den-files-tree__row gap-2 pr-2"
      classList={{ "den-files-tree__row--busy": props.busy }}
      style={{ "padding-left": props.paddingLeft }}
      data-files-ctx="no-menu"
    >
      <InlineRenameInput
        class="den-files-tree__name-input"
        testId="files-tree-rename-input"
        ariaLabel={`Rename ${props.name}`}
        initialValue={props.name}
        selectionRange={renameSelectionRange}
        shouldCommitOnBlur={(value) => {
          const resolved = renameEntryPath(props.path, value);
          return !("unchanged" in resolved || "error" in resolved);
        }}
        spellcheck={false}
        autocomplete="off"
        disabled={props.busy}
        onCommit={props.onSubmit}
        onCancel={props.onCancel}
      />
    </div>
  );
}

function useRowEngagement() {
  const [engaged, setEngaged] = createSignal(false);
  const engage = () => setEngaged(true);
  const disengage = (event: { currentTarget: HTMLElement; relatedTarget: EventTarget | null }) => {
    if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) return;
    setEngaged(false);
  };
  return { engaged, engage, disengage };
}

export function SwitchDisplayRow(props: { row: DisplayRow }) {
  const ctx = useTree();
  switch (props.row.kind) {
    case "entry":
      return props.row.entry.isDir ? (
        <DirEntryRow row={props.row.entry} />
      ) : (
        <FileEntryRow row={props.row.entry} />
      );
    case "naming":
      return <NamingRow display={props.row} />;
    case "create-error":
      return (
        <p
          class="den-files-tree__hint--error den-files-tree__hint"
          role="alert"
          data-testid="files-tree-new-entry-error"
          style={{ "padding-left": treeIndent(props.row.depth, ctx.fold(), 16) }}
        >
          {props.row.message}
        </p>
      );
    case "empty":
      return (
        <p
          class="den-files-tree__hint"
          style={{ "padding-left": treeIndent(props.row.depth, ctx.fold()) }}
        >
          Empty folder.
        </p>
      );
    case "error":
      return (
        <p
          class="den-files-tree__hint--error den-files-tree__hint"
          role="alert"
        >
          {props.row.message}
        </p>
      );
  }
}

/** All pinned rows use the same viewport coordinates and paint order. */
export function StickySlotBox(props: {
  slotKey: string;
  slots: () => readonly StickySlot[];
  tops: () => readonly number[];
}) {
  const index = () => props.slots().findIndex(slot => slot.key === props.slotKey);
  return (
    <ShowLatest when={props.slots()[index()]}>
      {slot => (
        <div
          class="den-files-tree-sticky__slot"
          style={{
            transform: `translateY(${props.tops()[index()] ?? 0}px)`,
            // Outer rows paint above inner rows.
            "z-index": String(props.slots().length - index()),
          }}
        >
          <StickySlotRow slot={slot()} />
        </div>
      )}
    </ShowLatest>
  );
}

function StickySlotRow(props: { slot: StickySlot }) {
  return (
    <Switch>
      <Match when={props.slot.kind === "folder" ? props.slot.ancestor : undefined}>
        {ancestor => <DirEntryRow row={ancestor().entry} surface="sticky" displayIndex={ancestor().displayIndex} />}
      </Match>
      <Match when={props.slot.kind === "elision" ? props.slot.hidden : undefined}>
        {hidden => <StickyElisionRow hidden={hidden()} />}
      </Match>
    </Switch>
  );
}

/** Folds the folders between the pinned root and the innermost pinned folders. */
function StickyElisionRow(props: { hidden: readonly StickyAncestor[] }) {
  const ctx = useTree();
  const [menuAnchor, setMenuAnchor] = createSignal<HTMLElement | null>(null);
  const folderName = (ancestor: StickyAncestor) =>
    ancestor.entry.isRoot ? `@${ancestor.entry.name}` : ancestor.entry.name;
  const count = () => `${props.hidden.length} folders`;
  const path = () => props.hidden.map(folderName).join(" / ");
  const open = (event: MouseEvent & { currentTarget: HTMLButtonElement }) => {
    event.preventDefault();
    event.stopPropagation();
    setMenuAnchor(menuAnchor() ? null : event.currentTarget);
  };

  return (
    <div
      class="den-files-tree__row den-files-tree__row--sticky"
      style={{ "padding-left": treeIndent(ctx.fold().floor, ctx.fold()) }}
      data-files-ctx="no-menu"
      data-testid="files-tree-sticky-elision"
    >
      <button
        type="button"
        class="den-files-tree__elision"
        tabindex={-1}
        aria-haspopup="menu"
        aria-expanded={menuAnchor() !== null}
        aria-label={`${count()} hidden: ${path()}`}
        onClick={open}
        onContextMenu={open}
      >
        <span class="den-files-tree__elision-mark" aria-hidden="true">⋯</span>
        <span class="den-files-tree__name">{count()}</span>
      </button>
      <Show when={menuAnchor()} keyed>
        {anchor => (
          <ContextMenu
            anchor={anchor}
            label="Hidden folders"
            items={props.hidden.map(ancestor => ({
              label: folderName(ancestor),
              testId: "files-tree-sticky-elision-folder",
              onSelect: () => ctx.activateStickyFolder(ancestor.displayIndex, ancestor.entry.rootId, ancestor.entry.path),
            }))}
            onDismiss={() => setMenuAnchor(null)}
          />
        )}
      </Show>
    </div>
  );
}

function DirEntryRow(props: {
  row: FlatTreeRow;
  surface?: "tree" | "sticky";
  displayIndex?: number;
}) {
  const ctx = useTree();
  const row = () => props.row;
  const sticky = () => props.surface === "sticky";
  const isRoot = () => row().isRoot;
  const isRenamingSelf = createMemo(
    () =>
      !sticky() &&
      ctx.props.renaming?.rootId === row().rootId &&
      ctx.props.renaming?.path === row().path,
  );
  const isDropTarget = createMemo(
    () =>
      !sticky() &&
      ctx.dropTarget()?.rootId === row().rootId &&
      ctx.dropTarget()?.dir === row().path,
  );
  const isInvalidDropTarget = createMemo(
    () => isDropTarget() && ctx.dropAssessment()?.valid === false,
  );
  const isPendingTarget = createMemo(
    () =>
      !sticky() &&
      ctx.movePending()?.target.rootId === row().rootId &&
      ctx.movePending()?.target.dir === row().path,
  );
  const isDragSource = createMemo(
    () =>
      !sticky() &&
      ctx.dragSource()?.rootId === row().rootId &&
      ctx.dragSource()?.path === row().path,
  );
  const isSelected = createMemo(
    () =>
      ctx.props.selectedEntry?.kind === "folder" &&
      ctx.props.selectedEntry.rootId === row().rootId &&
      ctx.props.selectedEntry.path === row().path,
  );
  const expanded = () => row().expanded === true;
  const rowPaddingLeft = () => treeIndent(row().depth, ctx.fold());
  const { engaged, engage, disengage } = useRowEngagement();

  return (
    <Show
      when={!isRenamingSelf()}
      fallback={
        <>
          <RenameRow
            paddingLeft={rowPaddingLeft()}
            name={row().name}
            path={row().path}
            busy={ctx.renamingBusy()}
            onSubmit={(value) =>
              ctx.submitRename(row().rootId, row().path, value, row().isDir)
            }
            onCancel={() => ctx.cancelRename()}
          />
          <Show when={ctx.renameError()}>
            <p
              class="den-files-tree__hint--error den-files-tree__hint"
              role="alert"
              data-testid="files-tree-rename-error"
              style={{ "padding-left": rowPaddingLeft() }}
            >
              {ctx.renameError()}
            </p>
          </Show>
        </>
      }
    >
      <div
        class="den-files-tree__row"
        classList={{
          "den-files-tree__row--root": isRoot(),
          "den-files-tree__row--active": isSelected(),
          "den-files-tree__row--drop-target": isDropTarget(),
          "den-files-tree__row--drop-invalid": isInvalidDropTarget(),
          "den-files-tree__row--move-pending": isPendingTarget(),
          "den-files-tree__row--dragging": isDragSource(),
          "den-files-tree__row--sticky": sticky(),
        }}
        style={{ "padding-left": rowPaddingLeft() }}
        data-files-ctx="tree-row"
        data-testid={sticky() ? "files-tree-sticky-dir" : "files-tree-dir"}
        data-root={row().rootId}
        data-path={row().path}
        data-name={row().name}
        data-isdir="true"
        data-isroot={isRoot() ? "true" : "false"}
        onMouseOver={engage}
        onMouseOut={disengage}
        onFocusIn={engage}
        onFocusOut={disengage}
        onPointerDown={
          sticky()
            ? undefined
            : (e) =>
                ctx.onRowPointerDown(
                  row().rootId,
                  row().path,
                  row().name,
                  true,
                  e,
                )
        }
        onContextMenu={(e) => {
          e.preventDefault();
          e.stopPropagation();
          ctx.openRowMenu(e, {
            surface: "tree-row",
            rootId: row().rootId,
            path: row().path,
            name: row().name,
            isDir: true,
            isRoot: isRoot(),
          });
        }}
      >
        <button
          type="button"
          class="den-files-tree__twisty"
          classList={{ "den-files-tree__twisty--open": expanded() }}
          aria-label={
            expanded() ? `Collapse ${row().name}` : `Expand ${row().name}`
          }
          aria-expanded={expanded()}
          tabindex={-1}
          data-testid="files-tree-dir-toggle"
          data-root={row().rootId}
          data-dir={row().path}
          onClick={(e) =>
            void ctx.toggleDir(row().rootId, row().path, {
              recursiveCollapse: e.altKey,
            })
          }
        >
          <span class="den-files-tree__twisty-mark" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="den-files-tree__label den-files-tree__label--dir"
          aria-expanded={expanded()}
          aria-current={isSelected() ? "true" : undefined}
          aria-keyshortcuts="Shift+F10"
          tabindex={sticky() || !ctx.isTreeTabStop(row()) ? -1 : 0}
          data-root={row().rootId}
          data-path={row().path}
          onFocus={() => {
            if (!sticky()) ctx.rememberTreeFocus(row());
          }}
          onClick={() => {
            if (sticky()) {
              const idx = props.displayIndex;
              if (idx != null) {
                ctx.activateStickyFolder(idx, row().rootId, row().path);
              }
              return;
            }
            void ctx.selectFolder(row().rootId, row().path);
          }}
        >
          <Show when={!isRoot()}>
            <FolderIcon open={expanded()} />
          </Show>
          <span
            class="den-files-tree__name"
            data-file-change={row().deleted ? "deleted" : undefined}
          >
            {isRoot() ? `@${row().name}` : row().name}
          </span>
          <Show when={row().deleted}>
            <FilesScopeChangeMark change={{ kind: "deleted", subject: "view" }} surface="tree" />
          </Show>
        </button>
        <Show when={isDropTarget() || isPendingTarget()}>
          <span class="den-files-tree__drop-label" aria-hidden="true">
            {isPendingTarget()
              ? "Moving…"
              : isInvalidDropTarget()
                ? "Not available"
                : "Move here"}
          </span>
        </Show>
        <Show when={!sticky() && engaged()}>
          <button
            type="button"
            class="den-files-tree__add"
            tabindex={-1}
            data-tip={`New file in ${row().name}`}
            data-tip-pos="below"
            aria-label={`New file in ${row().name}`}
            data-testid="files-tree-new-file"
            data-root={row().rootId}
            data-dir={row().path}
            onClick={() =>
              void ctx.startNaming(row().rootId, row().path, "file")
            }
          >
            <NewFileIcon />
          </button>
          <button
            type="button"
            class="den-files-tree__add"
            tabindex={-1}
            data-tip={`New folder in ${row().name}`}
            data-tip-pos="below"
            aria-label={`New folder in ${row().name}`}
            data-testid="files-tree-new-folder"
            data-root={row().rootId}
            data-dir={row().path}
            onClick={() =>
              void ctx.startNaming(row().rootId, row().path, "folder")
            }
          >
            <NewFolderIcon />
          </button>
          <button
            type="button"
            class="den-files-tree__more"
            tabindex={-1}
            aria-label={`More actions for ${row().name}`}
            aria-haspopup="menu"
            onClick={(e) =>
              ctx.openRowMenu(e, {
                surface: "tree-row",
                rootId: row().rootId,
                path: row().path,
                name: row().name,
                isDir: true,
                isRoot: isRoot(),
              })
            }
            onContextMenu={(e) =>
              ctx.openRowMenu(e, {
                surface: "tree-row",
                rootId: row().rootId,
                path: row().path,
                name: row().name,
                isDir: true,
                isRoot: isRoot(),
              })
            }
          >
            ⋯
          </button>
        </Show>
      </div>
    </Show>
  );
}

function FileEntryRow(props: { row: FlatTreeRow }) {
  const ctx = useTree();
  const row = () => props.row;
  const isSelected = createMemo(
    () =>
      ctx.props.selectedEntry?.kind === "file" &&
      ctx.props.selectedEntry.rootId === row().rootId &&
      ctx.props.selectedEntry.path === row().path,
  );
  const isRenamingSelf = createMemo(
    () =>
      ctx.props.renaming?.rootId === row().rootId &&
      ctx.props.renaming?.path === row().path,
  );
  const isDragSource = createMemo(
    () =>
      ctx.dragSource()?.rootId === row().rootId &&
      ctx.dragSource()?.path === row().path,
  );
  const isPendingSource = createMemo(
    () =>
      ctx.movePending()?.source.rootId === row().rootId &&
      ctx.movePending()?.source.path === row().path,
  );
  const rowPaddingLeft = () => treeIndent(row().depth, ctx.fold(), 16);
  const { engaged, engage, disengage } = useRowEngagement();

  /** Preserves memo identity when marks are unchanged. */
  const trailing = createMemo<RowTrailing>(
    () => {
      const changeKind = scopeChangeKindFromFlags({
        inScope: Boolean(ctx.props.inScope?.(row().rootId, row().path)),
        added: Boolean(ctx.props.addedInScope?.(row().rootId, row().path)),
        deleted: Boolean(row().deleted),
      });
      // Deleted outranks presence.
      if (changeKind === "deleted") {
        return { kind: "scope", changeKind: "deleted" };
      }
      const chip = ctx.props.agentPresenceForPath?.(row().rootId, row().path)?.chip;
      const state = trailingTabState({
        presence: Boolean(chip),
        dirty: false,
        changeKind,
      });
      if (state === "presence" && chip) {
        return { kind: "presence", chip };
      }
      if (
        state === "added" ||
        state === "changed" ||
        state === "deleted"
      ) {
        return { kind: "scope", changeKind: state };
      }
      return { kind: "none" };
    },
    { kind: "none" },
    { equals: sameTrailing },
  );

  const trailingChip = () => {
    const t = trailing();
    return t.kind === "presence" ? t.chip : null;
  };

  const agentName = () => ctx.props.agentPresenceForPath?.(row().rootId, row().path)?.name ?? null;

  const trailingScopeKind = () => {
    const t = trailing();
    return t.kind === "scope" ? t.changeKind : null;
  };

  /** Name tint follows scope even when presence outranks the trailing glyph. */
  const nameScopeKind = (): ScopeChangeKind | null => {
    return scopeChangeKindFromFlags({
      inScope: Boolean(ctx.props.inScope?.(row().rootId, row().path)),
      added: Boolean(ctx.props.addedInScope?.(row().rootId, row().path)),
      deleted: Boolean(row().deleted),
    });
  };

  return (
    <Show
      when={!isRenamingSelf()}
      fallback={
        <>
          <RenameRow
            paddingLeft={rowPaddingLeft()}
            name={row().name}
            path={row().path}
            busy={ctx.renamingBusy()}
            onSubmit={(value) =>
              ctx.submitRename(row().rootId, row().path, value, row().isDir)
            }
            onCancel={() => ctx.cancelRename()}
          />
          <Show when={ctx.renameError()}>
            <p
              class="den-files-tree__hint--error den-files-tree__hint"
              role="alert"
              data-testid="files-tree-rename-error"
              style={{ "padding-left": rowPaddingLeft() }}
            >
              {ctx.renameError()}
            </p>
          </Show>
        </>
      }
    >
      <div
        class="den-files-tree__row den-files-tree__row--file"
        classList={{
          "den-files-tree__row--active": isSelected(),
          "den-files-tree__row--dragging": isDragSource(),
          "den-files-tree__row--move-source-pending": isPendingSource(),
        }}
        style={{ "padding-left": rowPaddingLeft() }}
        data-files-ctx="tree-row"
        data-root={row().rootId}
        data-path={row().path}
        data-name={row().name}
        data-isdir="false"
        data-isroot="false"
        data-deleted={row().deleted ? "true" : undefined}
        onMouseOver={engage}
        onMouseOut={disengage}
        onFocusIn={engage}
        onFocusOut={disengage}
        onPointerDown={(e) =>
          ctx.onRowPointerDown(row().rootId, row().path, row().name, false, e)
        }
      >
        <button
          type="button"
          class="den-files-tree__label den-files-tree__label--file"
          data-testid="files-tree-file"
          data-root={row().rootId}
          data-path={row().path}
          aria-current={isSelected() ? "true" : undefined}
          aria-keyshortcuts="Shift+F10"
          tabindex={ctx.isTreeTabStop(row()) ? 0 : -1}
          onFocus={() => ctx.rememberTreeFocus(row())}
          onClick={(event) => {
            // Ignore the second click of a double-click gesture.
            if (event.detail > 1) return;
            ctx.openFile(
              {
                rootId: row().rootId,
                rootLabel: row().rootLabel,
                path: row().path,
              },
              "permanent",
            );
          }}
        >
          <FileIcon />
          <span
            class="den-files-tree__name"
            classList={{
              "den-files-agent-name": agentName() !== null,
            }}
            data-file-change={nameScopeKind() ?? undefined}
            style={(() => {
              const name = agentName();
              return name ? agentNameStyle(name.chat) : undefined;
            })()}
            data-agent-session={agentName()?.chat.sessionId}
            data-tip={agentName()?.labels.join("; ")}
          >
            {row().name}
          </span>
          <Switch>
            <Match when={trailingChip()}>
              {(chip) => <FilesAgentChip chip={chip()} testId="files-tree-presence" />}
            </Match>
            <Match keyed when={trailingScopeKind()}>
              {(kind) => (
                <FilesScopeChangeMark change={{ kind, subject: "view" }} surface="tree" />
              )}
            </Match>
          </Switch>
        </button>
        <Show when={Boolean(row().deleted && ctx.props.onRevertFile)}>
          <button
            type="button"
            class="den-files-tree__revert"
            tabindex={-1}
            data-testid="files-tree-revert"
            aria-label={`Revert deletion of ${row().name}`}
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              ctx.props.onRevertFile?.(row().rootId, row().path);
            }}
          >
            Revert
          </button>
        </Show>
        <Show when={engaged()}>
          <button
            type="button"
            class="den-files-tree__more"
            tabindex={-1}
            aria-label={`More actions for ${row().name}`}
            aria-haspopup="menu"
            onClick={(e) =>
              ctx.openRowMenu(e, {
                surface: "tree-row",
                rootId: row().rootId,
                path: row().path,
                name: row().name,
                isDir: false,
                isRoot: false,
                deleted: Boolean(row().deleted),
              })
            }
            onContextMenu={(e) =>
              ctx.openRowMenu(e, {
                surface: "tree-row",
                rootId: row().rootId,
                path: row().path,
                name: row().name,
                isDir: false,
                isRoot: false,
                deleted: Boolean(row().deleted),
              })
            }
          >
            ⋯
          </button>
        </Show>
      </div>
    </Show>
  );
}

function NamingRow(props: {
  display: Extract<DisplayRow, { kind: "naming" }>;
}) {
  const ctx = useTree();
  const d = () => props.display;
  let nameEl: HTMLInputElement | undefined;
  const st = () => ctx.dirState(d().rootId, d().dir);
  onMount(() => nameEl?.focus());
  return (
    <div
      class="den-files-tree__row gap-2 pr-2"
      style={{ "padding-left": treeIndent(d().depth + 1, ctx.fold(), 16) }}
      data-files-ctx="no-menu"
    >
      <Show when={d().entryKind === "folder"} fallback={<FileIcon />}>
        <FolderIcon />
      </Show>
      <input
        ref={nameEl}
        type="text"
        class="den-files-tree__name-input"
        data-testid="files-tree-new-entry-input"
        data-files-ctx="no-menu"
        aria-label={
          d().entryKind === "folder"
            ? `New folder name in ${d().dir === "." ? d().rootLabel : d().dir}`
            : `New file name in ${d().dir === "." ? d().rootLabel : d().dir}`
        }
        placeholder={d().entryKind === "folder" ? "folder-name" : "name.ts"}
        spellcheck={false}
        autocomplete="off"
        disabled={st().creating}
        value={st().namingDraft}
        onInput={(e) =>
          ctx.setNamingDraft(d().rootId, d().dir, e.currentTarget.value)
        }
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            void ctx.submitName(
              d().rootId,
              d().rootLabel,
              d().dir,
              e.currentTarget.value,
            );
          } else if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            ctx.cancelNaming(d().rootId, d().dir);
          }
        }}
        onBlur={(e) => {
          if (!e.currentTarget.value.trim() && !st().creating) {
            ctx.cancelNaming(d().rootId, d().dir);
          }
        }}
      />
    </div>
  );
}
