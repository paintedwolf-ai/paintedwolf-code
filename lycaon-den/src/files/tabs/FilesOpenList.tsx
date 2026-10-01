import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
} from "solid-js";
import type { ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { PinIcon } from "../../components/primitives/PinIcon.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { isSubsequence } from "../tree/file-inventory.ts";
import { filesTabNames } from "./files-tab-names.ts";
import { fileTabKind, specialTabKind } from "./files-tab-kind.ts";
import { FilesTabKindIcon } from "./FilesTab.tsx";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import { FilesScopeChangeMark } from "../components/FilesScopeChangeMark.tsx";
import { FilesAgentChip } from "../components/FilesAgentChip.tsx";
import { agentNameStyle, type FilesAgentPresence } from "../components/files-agent-presence.ts";
import type { FileChange } from "../components/files-scope-change-mark.ts";
import { handlerMatchesEvent } from "../../shortcuts/display-binding-for.ts";
import { createOverlayShortcutScope } from "../../platform/interaction/modal-focus-trap.ts";

export type FilesOpenListRow = {
  key: FileBufferKey;
  buffer: FileBuffer;
  agent?: FilesAgentPresence | null;
  change?: FileChange | null;
  previousVersion?: boolean;
};

type Props = {
  open: boolean;
  rows: FilesOpenListRow[];
  activeKey: FileBufferKey | null;
  anchorEl: HTMLElement | null;
  onClose: () => void;
  onActivate: (key: FileBufferKey) => void;
  onCloseBuffer: (key: FileBufferKey) => void;
  onRowContextMenu: (key: FileBufferKey, anchor: ContextMenuAnchor) => void;
};

function dirnameOf(path: string): string {
  const i = path.lastIndexOf("/");
  return i <= 0 ? "" : path.slice(0, i);
}

function kindLabel(buffer: FileBuffer): string | null {
  if (buffer.loading || buffer.loadError) return null;
  if (buffer.kind === "image") return "Image";
  if (buffer.kind === "info") {
    if (buffer.unsupportedEncodingDetected) return "Encoding";
    if (buffer.overLimit) return "Too large";
    return "Binary";
  }
  return null;
}

export function FilesOpenList(props: Props) {
  const [query, setQuery] = createSignal("");
  const [highlight, setHighlight] = createSignal(0);
  let filterEl: HTMLInputElement | undefined;

  createOverlayShortcutScope(() => props.open);

  const names = createMemo(() => filesTabNames(props.rows.map(row => row.buffer)));
  const filtered = createMemo(() => {
    const q = query().trim().toLowerCase();
    const rows = props.rows;
    if (!q) return rows;
    return rows.filter((row) => {
      const name = row.buffer.name.toLowerCase();
      const path = row.buffer.path.toLowerCase();
      return (
        isSubsequence(name, q) || isSubsequence(path, q) ||
        isSubsequence(fileTabKind(row.buffer, row.previousVersion)?.label.toLowerCase() ?? "", q)
      );
    });
  });

  createEffect(() => {
    void filtered().length;
    setHighlight(0);
  });

  createEffect(() => {
    if (props.open) {
      setQuery("");
      setHighlight(0);
      queueMicrotask(() => filterEl?.focus());
    }
  });

  const move = (dir: 1 | -1) => {
    const list = filtered();
    if (list.length === 0) return;
    setHighlight((h) => (h + dir + list.length) % list.length);
  };

  const activateHighlighted = () => {
    const row = filtered()[highlight()];
    if (!row) return;
    props.onActivate(row.key);
    props.onClose();
  };

  return (
    <Show when={props.open}>
      <AnchoredSurface
        class="den-files-open-list"
        role="dialog"
        ariaLabel="Open files"
        testId="files-open-list"
        anchor={() => props.anchorEl}
        preferredSide="bottom"
        align="end"
        overflow="hidden"
        dismissOnEscape={false}
        onDismiss={props.onClose}
        onKeyDown={(e) => {
          if (handlerMatchesEvent("list.down", e)) {
            e.preventDefault();
            move(1);
          } else if (handlerMatchesEvent("list.up", e)) {
            e.preventDefault();
            move(-1);
          } else if (handlerMatchesEvent("list.confirm", e)) {
            e.preventDefault();
            activateHighlighted();
          } else if (handlerMatchesEvent("overlay.dismiss", e)) {
            e.preventDefault();
            props.onClose();
            props.anchorEl?.focus();
          } else if (handlerMatchesEvent("files.closeTab", e)) {
            e.preventDefault();
            const row = filtered()[highlight()];
            if (row) props.onCloseBuffer(row.key);
          }
        }}
      >
        <input
          ref={filterEl}
          type="search"
          class="den-files-open-list__filter"
          data-testid="files-open-list-filter"
          placeholder="Filter open files"
          value={query()}
          onInput={(e) => setQuery(e.currentTarget.value)}
        />
        <Show
          when={filtered().length > 0}
          fallback={
            <p class="den-files-open-list__empty" data-testid="files-open-list-empty">
              No open files match "{query().trim()}"
            </p>
          }
        >
          <Scrollport
            class="den-files-open-list__rows"
            contentClass="den-files-open-list__rows-body"
            content={{ role: "list", "aria-label": "Open files" }}
          >
              <For each={filtered()}>
                {(row, i) => {
                  const special = () => specialTabKind(row.buffer);
                  const mark = () => fileTabKind(row.buffer, row.previousVersion);
                  const dir = () => special() ? "" : names().get(row.key)?.qualifier || dirnameOf(row.buffer.path);
                  const glyph = () => kindLabel(row.buffer);
                  const showSep = () => {
                    if (i() === 0) return false;
                    const prev = filtered()[i() - 1];
                    return !!prev?.buffer.pinned && !row.buffer.pinned;
                  };
                  return (
                    <div
                      class="den-files-open-list__row"
                      classList={{
                        "den-files-open-list__row--active":
                          row.key === props.activeKey,
                        "den-files-open-list__row--highlight":
                          i() === highlight(),
                        "den-files-open-list__row--preview": row.buffer.preview,
                        "den-files-open-list__row--pinned": row.buffer.pinned,
                        "den-files-open-list__row--after-pin": showSep(),
                      }}
                      role="listitem"
                      data-testid="files-open-list-row"
                      data-key={row.key}
                      data-pin-sep={showSep() ? "true" : undefined}
                      onMouseEnter={() => setHighlight(i())}
                      onContextMenu={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        props.onRowContextMenu(row.key, {
                          x: e.clientX,
                          y: e.clientY,
                        });
                      }}
                    >
                      <Show when={showSep()}>
                        <span
                          class="den-files-open-list__sep"
                          aria-hidden="true"
                          data-testid="files-open-list-pin-sep"
                        />
                      </Show>
                      <button
                        type="button"
                        class="den-files-open-list__activate"
                        aria-label={[
                          names().get(row.key)?.label ?? row.buffer.name,
                          mark()?.label,
                          row.buffer.preview ? "Preview" : null,
                          row.buffer.pinned ? "Pinned" : null,
                        ].filter(Boolean).join(", ")}
                        aria-current={
                          row.key === props.activeKey ? "true" : undefined
                        }
                        onClick={() => {
                          props.onActivate(row.key);
                          props.onClose();
                        }}
                      >
                        <Show when={row.buffer.pinned}>
                          <span
                            class="den-files-pin-glyph"
                            aria-hidden="true"
                            data-testid="files-open-list-pin"
                          >
                            <PinIcon size={10} />
                          </span>
                        </Show>
                        <FilesTabKindIcon buffer={row.buffer} previousVersion={row.previousVersion} showTooltip={false} />
                        <Show when={glyph()}>
                          {(g) => (
                            <span
                              class="den-files-open-list__kind"
                              aria-hidden="true"
                            >
                              {g()}
                            </span>
                          )}
                        </Show>
                        <span
                          class="den-files-open-list__name"
                          classList={{
                            "den-files-agent-name": Boolean(row.agent?.name),
                          }}
                          data-file-change={row.change?.kind}
                          style={row.agent?.name ? agentNameStyle(row.agent.name.chat) : undefined}
                        >
                          {row.buffer.name}
                        </span>
                        <Show when={dir()}>
                          {(d) => (
                            <span class="den-files-open-list__dir">{d()}</span>
                          )}
                        </Show>
                        <Show when={row.agent?.chip} keyed>
                          {(chip) => <FilesAgentChip chip={chip} testId="files-open-list-presence" showTooltip={false} />}
                        </Show>
                        <Show when={row.buffer.dirty}>
                          <span
                            class="den-files-tab__dirty"
                            data-testid="files-open-list-dirty"
                            aria-label="Unsaved changes"
                          />
                        </Show>
                        <Show keyed when={row.change}>
                          {(change) => (
                            <FilesScopeChangeMark
                              change={change}
                              surface="open-list"
                            />
                          )}
                        </Show>
                      </button>
                      <button
                        type="button"
                        class="den-files-open-list__close"
                        data-testid="files-open-list-close"
                        aria-label={`Close ${names().get(row.key)?.label ?? row.buffer.name}`}
                        onClick={(e) => {
                          e.stopPropagation();
                          props.onCloseBuffer(row.key);
                        }}
                      >
                        ×
                      </button>
                    </div>
                  );
                }}
              </For>
          </Scrollport>
        </Show>
      </AnchoredSurface>
    </Show>
  );
}
