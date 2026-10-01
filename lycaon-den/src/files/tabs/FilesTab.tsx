import { Show } from "solid-js";
import { PinIcon } from "../../components/primitives/PinIcon.tsx";
import type { ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import { tauriDragRegionProps } from "../../platform/runtime.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import type { FileChange } from "../components/files-scope-change-mark.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import type { FileTabName } from "./files-tab-names.ts";
import { fileTabKind, specialTabKind, type FileTabIdentity } from "./files-tab-kind.ts";
import { splitFileNameForMiddleEllipsis } from "./files-tab-strip.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { focusRegion } from "../../shortcuts/focus-region.ts";

type FilesTabProps = {
  buffer: FileBuffer;
  name: FileTabName | undefined;
  stateLabels: string[];
  nameChange: () => FileChange | null;
  active: boolean;
  pending: boolean;
  focusable: boolean;
  dragging: boolean;
  dropBefore: boolean;
  dropAfter: boolean;
  previousVersion: boolean;
  merging: boolean;
  onPointerDown: (key: FileBufferKey, event: PointerEvent) => void;
  onActivate: (key: FileBufferKey) => void;
  onPromote: (key: FileBufferKey) => void;
  onContextMenu: (key: FileBufferKey, anchor: ContextMenuAnchor) => void;
  onClose: (key: FileBufferKey) => void;
};

export function FilesTab(props: FilesTabProps) {
  const b = props.buffer;
  const key = b.key;
  return (
    <div
      class="den-files-tab"
      classList={{
        "den-files-tab--active": props.active,
        "den-files-tab--preview": b.preview,
        "den-files-tab--pinned": b.pinned,
        "den-files-tab--close-failed":
          b.closeError != null,
        "den-files-tab--dragging":
          props.dragging,
        "den-files-tab--drop-before":
          props.dropBefore,
        "den-files-tab--drop-after":
          props.dropAfter,
      }}
      data-files-ctx="tab"
      data-key={key}
      data-root={b.rootId}
      data-path={b.path}
      data-name={b.name}
      data-tauri-drag-region="false"
      onPointerDown={[props.onPointerDown, key]}
    >
      <div class="den-files-tab__header" aria-hidden="true" {...tauriDragRegionProps({ deep: true })} />
      <div class="den-files-tab__body">
        <button
          type="button"
          class="den-files-tab__label"
          role="tab"
          aria-selected={props.active}
          aria-label={[props.name?.label ?? b.name, ...props.stateLabels].join(", ")}
          aria-busy={props.active && props.pending}
          aria-description={b.closeError ?? undefined}
          tabindex={
            props.focusable
              ? 0
              : -1
          }
          data-testid="files-tab"
          data-path={b.path}
          onClick={() => props.onActivate(key)}
          onDblClick={() =>
            props.onPromote(key)
          }
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              props.onActivate(key);
              focusRegion("files");
            } else if (
              e.key === "F10" &&
              e.shiftKey
            ) {
              e.preventDefault();
              const rect =
                e.currentTarget.getBoundingClientRect();
              props.onContextMenu(key, {
                x: rect.left,
                y: rect.bottom,
              });
            }
          }}
          onMouseDown={(e) => {
            if (e.button === 1) e.preventDefault();
          }}
          onAuxClick={(e) => {
            if (e.button === 1) props.onClose(key);
          }}
        >
          <Show when={b.pinned}>
            <span
              class="den-files-pin-glyph"
              aria-hidden="true"
              data-testid="files-tab-pin"
            >
              <PinIcon size={10} />
            </span>
          </Show>
          <FilesTabKindIcon buffer={b} previousVersion={props.previousVersion} />
          {(() => {
            const parts =
              specialTabKind(b)
                ? { start: b.name, end: "" }
                : splitFileNameForMiddleEllipsis(b.name);
            const nameChange = props.nameChange();
            return (
              <>
                <span
                  class="den-files-tab__name"
                  data-file-change={nameChange?.kind}
                >
                  <span class="den-files-tab__name-start">
                    {parts.start}
                  </span>
                  <Show when={parts.end}>
                    <span class="den-files-tab__name-end">
                      {parts.end}
                    </span>
                  </Show>
                </span>
              </>
            );
          })()}
          <Show when={props.name?.qualifier}>
            {(qualifier) => <span class="den-files-tab__qualifier">{qualifier()}</span>}
          </Show>
          <span
            class="den-files-tab__dirty"
            classList={{ "den-files-tab__dirty--visible": b.dirty }}
            data-testid="files-tab-dirty"
            aria-label={b.dirty ? "Unsaved changes" : undefined}
            aria-hidden={!b.dirty}
          />
          <Show
            when={
              props.merging
            }
          >
            <span
              class="den-files-tab__merge"
              data-testid="files-tab-merge"
              aria-label="Merge in progress"
            >
              merge
            </span>
          </Show>
        </button>
        <button
          type="button"
          class="den-files-tab__close"
          tabindex={props.focusable ? 0 : -1}
          aria-label={`Close ${props.name?.label ?? b.name}`}
          data-testid="files-tab-close"
          onClick={() => props.onClose(key)}
        >
          ×
        </button>
      </div>
    </div>
  );
}

export function FilesTabKindIcon(props: { buffer: FileTabIdentity; previousVersion?: boolean; showTooltip?: boolean }) {
  return (
    <Show keyed when={fileTabKind(props.buffer, props.previousVersion)}>
      {(kind) => (
        <span class="den-files-tab-kind-icon" data-tip={props.showTooltip === false ? undefined : kind.label}>
          <ThemeIcon slot={kind.icon} size={14} />
        </span>
      )}
    </Show>
  );
}
