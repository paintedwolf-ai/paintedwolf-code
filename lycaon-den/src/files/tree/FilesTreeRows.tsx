import { Match, Show, Switch, createMemo, onMount } from "solid-js";
import { FilesScopeChangeMark } from "../components/FilesScopeChangeMark.tsx";
import { type FlatTreeRow } from "./project-files-tree-flat.ts";
import type { DisplayRow } from "./files-tree-display-model.ts";
import { type StickySlot } from "./files-tree-sticky.ts";
import { useTree } from "./files-tree-context.ts";
import { isImeComposition } from "../../platform/interaction/ime-composition.ts";
import {
  FileEntryRow,
  FileIcon,
  FolderIcon,
  NewFileIcon,
  NewFolderIcon,
  RenameRow,
  ShowLatest,
  StickyElisionRow,
  treeIndent,
  useRowEngagement,
} from "./FilesTreeRowItem.tsx";

export function SwitchDisplayRow(props: { row: DisplayRow }) {
  const ctx = useTree();
  switch (props.row.kind) {
    case "entry":
      return props.row.entry.isDir ? (
        <DirEntryRow row={props.row.entry} />
      ) : (
        <FileEntryRow
          row={props.row.entry}
          renameFallback={(paddingLeft, entry) => (
            <>
              <RenameRow
                paddingLeft={paddingLeft}
                name={entry().name}
                path={entry().path}
                busy={ctx.renamingBusy()}
                onSubmit={(value) => ctx.submitRename(entry().rootId, entry().path, value, entry().isDir)}
                onCancel={() => ctx.cancelRename()}
              />
              <Show when={ctx.renameError()}>
                <p
                  class="den-files-tree__hint--error den-files-tree__hint"
                  role="alert"
                  data-testid="files-tree-rename-error"
                  style={{ "padding-left": paddingLeft }}
                >
                  {ctx.renameError()}
                </p>
              </Show>
            </>
          )}
        />
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
        <p class="den-files-tree__hint" style={{ "padding-left": treeIndent(props.row.depth, ctx.fold()) }}>
          Empty folder.
        </p>
      );
    case "error":
      return (
        <p class="den-files-tree__hint--error den-files-tree__hint" role="alert">
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
    () => !sticky() && ctx.props.renaming?.rootId === row().rootId && ctx.props.renaming?.path === row().path,
  );
  const isDropTarget = createMemo(
    () => !sticky() && ctx.dropTarget()?.rootId === row().rootId && ctx.dropTarget()?.dir === row().path,
  );
  const isInvalidDropTarget = createMemo(() => isDropTarget() && ctx.dropAssessment()?.valid === false);
  const isPendingTarget = createMemo(
    () => !sticky() && ctx.movePending()?.target.rootId === row().rootId && ctx.movePending()?.target.dir === row().path,
  );
  const isDragSource = createMemo(
    () => !sticky() && ctx.dragSource()?.rootId === row().rootId && ctx.dragSource()?.path === row().path,
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
            onSubmit={(value) => ctx.submitRename(row().rootId, row().path, value, row().isDir)}
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
            : (e) => ctx.onRowPointerDown(row().rootId, row().path, row().name, true, e)
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
          aria-label={expanded() ? `Collapse ${row().name}` : `Expand ${row().name}`}
          aria-expanded={expanded()}
          tabindex={-1}
          data-testid="files-tree-dir-toggle"
          data-root={row().rootId}
          data-dir={row().path}
          onClick={(e) => void ctx.toggleDir(row().rootId, row().path, { recursiveCollapse: e.altKey })}
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
              if (idx != null) ctx.activateStickyFolder(idx, row().rootId, row().path);
              return;
            }
            void ctx.selectFolder(row().rootId, row().path);
          }}
        >
          <Show when={!isRoot()}>
            <FolderIcon open={expanded()} />
          </Show>
          <span class="den-files-tree__name" data-file-change={row().deleted ? "deleted" : undefined}>
            {isRoot() ? `@${row().name}` : row().name}
          </span>
          <Show when={row().deleted}>
            <FilesScopeChangeMark change={{ kind: "deleted", subject: "view" }} surface="tree" />
          </Show>
        </button>
        <Show when={isDropTarget() || isPendingTarget()}>
          <span class="den-files-tree__drop-label" aria-hidden="true">
            {isPendingTarget() ? "Moving…" : isInvalidDropTarget() ? "Not available" : "Move here"}
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
            onClick={() => void ctx.startNaming(row().rootId, row().path, "file")}
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
            onClick={() => void ctx.startNaming(row().rootId, row().path, "folder")}
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

function NamingRow(props: { display: Extract<DisplayRow, { kind: "naming" }> }) {
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
        onInput={(e) => ctx.setNamingDraft(d().rootId, d().dir, e.currentTarget.value)}
        onKeyDown={(e) => {
          if (isImeComposition(e)) return;
          if (e.key === "Enter") {
            e.preventDefault();
            void ctx.submitName(d().rootId, d().rootLabel, d().dir, e.currentTarget.value);
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
