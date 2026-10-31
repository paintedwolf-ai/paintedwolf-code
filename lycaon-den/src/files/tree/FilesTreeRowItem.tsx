import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { ContextMenu } from "../../components/ContextMenu.tsx";
import { Match, Show, Switch, createMemo, createSignal, type JSX } from "solid-js";
import { InlineRenameInput } from "../../components/inline-rename/InlineRenameInput.tsx";
import { renameEntryPath, renameSelectionRange } from "../commands/project-files-create.ts";
import { FilesScopeChangeMark } from "../components/FilesScopeChangeMark.tsx";
import { scopeChangeKindFromFlags, type ScopeChangeKind } from "../components/files-scope-change-mark.ts";
import { trailingTabState } from "../tabs/files-tab-trailing.ts";
import { FilesAgentChip } from "../components/FilesAgentChip.tsx";
import { agentNameStyle, type FilesAgentChip as FilesAgentChipValue } from "../components/files-agent-presence.ts";
import { type FlatTreeRow } from "./project-files-tree-flat.ts";
import { foldedTreeDepth, type StickyAncestor, type StickyFold } from "./files-tree-sticky.ts";
import { useTree } from "./files-tree-context.ts";

export { ShowLatest };

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

export function FolderIcon(props: { open?: boolean }) {
  return <ThemeIcon slot={props.open ? "folder-open" : "folder"} size={14} class="den-files-tree__icon" />;
}

export function NewFileIcon() {
  return <ThemeIcon slot="new-file" size={13} class="den-files-tree__icon" />;
}

export function NewFolderIcon() {
  return <ThemeIcon slot="new-folder" size={14} class="den-files-tree__icon" />;
}

export function FileIcon() {
  return <ThemeIcon slot="file" size={14} class="den-files-tree__icon" />;
}

/** Indentation uses the displayed fold before rows mount. */
export function treeIndent(depth: number, fold: StickyFold, offset = 0): string {
  return `${foldedTreeDepth(depth, fold) * 14 + offset}px`;
}

export function RenameRow(props: {
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

export function useRowEngagement() {
  const [engaged, setEngaged] = createSignal(false);
  const engage = () => setEngaged(true);
  const disengage = (event: { currentTarget: HTMLElement; relatedTarget: EventTarget | null }) => {
    if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) return;
    setEngaged(false);
  };
  return { engaged, engage, disengage };
}

/** Folds the folders between the pinned root and the innermost pinned folders. */
export function StickyElisionRow(props: { hidden: readonly StickyAncestor[] }) {
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

export function FileEntryRow(props: {
  row: FlatTreeRow;
  renameFallback: (paddingLeft: string, row: () => FlatTreeRow) => JSX.Element;
}) {
  const ctx = useTree();
  const row = () => props.row;
  const isSelected = createMemo(
    () =>
      ctx.props.selectedEntry?.kind === "file" &&
      ctx.props.selectedEntry.rootId === row().rootId &&
      ctx.props.selectedEntry.path === row().path,
  );
  const isRenamingSelf = createMemo(
    () => ctx.props.renaming?.rootId === row().rootId && ctx.props.renaming?.path === row().path,
  );
  const isDragSource = createMemo(
    () => ctx.dragSource()?.rootId === row().rootId && ctx.dragSource()?.path === row().path,
  );
  const isPendingSource = createMemo(
    () => ctx.movePending()?.source.rootId === row().rootId && ctx.movePending()?.source.path === row().path,
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
      if (changeKind === "deleted") return { kind: "scope", changeKind: "deleted" };
      const chip = ctx.props.agentPresenceForPath?.(row().rootId, row().path)?.chip;
      const state = trailingTabState({ presence: Boolean(chip), dirty: false, changeKind });
      if (state === "presence" && chip) return { kind: "presence", chip };
      if (state === "added" || state === "changed" || state === "deleted") {
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
  const nameScopeKind = (): ScopeChangeKind | null =>
    scopeChangeKindFromFlags({
      inScope: Boolean(ctx.props.inScope?.(row().rootId, row().path)),
      added: Boolean(ctx.props.addedInScope?.(row().rootId, row().path)),
      deleted: Boolean(row().deleted),
    });

  return (
    <Show when={!isRenamingSelf()} fallback={props.renameFallback(rowPaddingLeft(), row)}>
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
        onPointerDown={(e) => ctx.onRowPointerDown(row().rootId, row().path, row().name, false, e)}
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
            ctx.openFile({ rootId: row().rootId, rootLabel: row().rootLabel, path: row().path }, "permanent");
          }}
        >
          <FileIcon />
          <span
            class="den-files-tree__name"
            classList={{ "den-files-agent-name": agentName() !== null }}
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
              {(kind) => <FilesScopeChangeMark change={{ kind, subject: "view" }} surface="tree" />}
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
