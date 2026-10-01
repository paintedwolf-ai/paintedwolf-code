import { registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";
import { For, Show, createMemo, onCleanup, type JSX } from "solid-js";
import { type ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import { SidebarAutomaticExpandIcon, SidebarCollapseIcon, SidebarExpandIcon } from "../../components/nav/NavSidebarToggle.tsx";
import { tauriDragRegionProps } from "../../platform/runtime.ts";
import type { FilesAgentPresence } from "../components/files-agent-presence.ts";
import type { FilesScope } from "../components/files-scope.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import { filesBufferChange } from "../documents/files-buffer-change.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { promoteFilesBuffer, setFilesActiveBuffer } from "../documents/project-files-buffers.ts";
import type { createFilesTabCommands } from "./files-tab-commands.ts";
import type { createFilesTabDrag } from "./files-tab-drag.ts";
import { fileTabKind, specialTabKind } from "./files-tab-kind.ts";
import { filesTabNames } from "./files-tab-names.ts";
import { trailingTabStateLabels } from "./files-tab-trailing.ts";
import { FilesOpenList } from "./FilesOpenList.tsx";
import { FilesTab } from "./FilesTab.tsx";

type StripShellProps = {
  treeCollapsed: boolean;
  side: "left" | "right";
  onToggleTree: () => void;
  onAutoRestore: () => void;
};

function FilesTabStripShell(props: StripShellProps & { children: JSX.Element }) {
  return (
    <div
      class="den-files-tab-strip"
      data-files-ctx="tab-strip"
      data-testid="files-tab-strip"
      {...tauriDragRegionProps({ deep: true })}
    >
      <button
        type="button"
        class="den-files-tab-strip__tree-toggle"
        data-restore="user"
        aria-label={
          props.treeCollapsed ? "Show file navigator" : "Hide file navigator"
        }
        aria-pressed={!props.treeCollapsed}
        data-testid="files-tree-toggle"
        onClick={props.onToggleTree}
      >
        <Show
          when={props.treeCollapsed}
          fallback={<SidebarCollapseIcon side={props.side} />}
        >
          <SidebarExpandIcon side={props.side} />
        </Show>
      </button>
      <button
        type="button"
        class="den-files-tab-strip__tree-toggle den-auto-restore-control"
        data-restore="automatic"
        aria-label="Widen window to restore file navigator"
        data-tip="Widen window to restore Files"
        data-tip-pos="below"
        data-testid="files-tree-auto-restore-btn"
        onClick={props.onAutoRestore}
      >
        <SidebarAutomaticExpandIcon side={props.side} />
      </button>
      {props.children}
    </div>
  );
}

/** With no open tabs the strip keeps its tree toggles and an empty tablist. */
export function EmptyFilesTabStrip(props: StripShellProps) {
  return (
    <FilesTabStripShell
      treeCollapsed={props.treeCollapsed}
      side={props.side}
      onToggleTree={props.onToggleTree}
      onAutoRestore={props.onAutoRestore}
    >
      <div class="den-files-tabs-wrap">
        <div class="den-files-tabs-scroller">
          <div
            class="den-files-tabs"
            role="tablist"
            aria-label="Open files"
            data-testid="files-tabs"
          />
        </div>
      </div>
    </FilesTabStripShell>
  );
}

/** Open tabs, their overflow count, and the open-files list; each tab's name and state labels are decided here. */
export function FilesTabStrip(props: StripShellProps & {
  scope: Pick<FilesScope, "projectId" | "state" | "stripOrder" | "aimedKey" | "editorPresentationPending" | "versionForBuffer">;
  tabs: ReturnType<typeof createFilesTabCommands>;
  drag: ReturnType<typeof createFilesTabDrag>;
  hiddenTabCount: number;
  showLeadingFade: boolean;
  showTrailingFade: boolean;
  /** Changes when review marks change, which relabels tabs. */
  scopeMarksVersion: () => number;
  previousVersionForBuffer: (buffer: FileBuffer) => boolean;
  agentPresenceForPath: (rootId: string, path: string) => FilesAgentPresence | null;
  mergingKey: FileBufferKey | undefined;
  requestCloseTab: (key: FileBufferKey) => void;
  onTabContextMenu: (key: FileBufferKey, anchor: ContextMenuAnchor) => void;
  onScrollerElement: (element: HTMLDivElement) => void;
  onTabsElement: (element: HTMLDivElement) => void;
}) {
  let tabsEl: HTMLDivElement | undefined;
  let overflowBtnEl: HTMLButtonElement | undefined;
  const openListAnchor = (): HTMLElement | null => overflowBtnEl ?? tabsEl ?? null;
  const state = () => props.scope.state();
  const change = (buffer: FileBuffer) => {
    void props.scopeMarksVersion();
    return filesBufferChange(props.scope.projectId(), buffer, props.scope.versionForBuffer(buffer));
  };
  const tabNames = createMemo(() => filesTabNames(state().order.flatMap(key => {
    const buffer = state().byKey[key];
    return buffer ? [buffer] : [];
  })));
  const stateLabelsFor = (buffer: FileBuffer): string[] => {
    void props.scopeMarksVersion();
    const kind = fileTabKind(buffer, props.previousVersionForBuffer(buffer))?.label;
    return [
      ...(kind ? [kind] : []),
      ...(!specialTabKind(buffer) && buffer.kind !== "diff" && buffer.jobId ? ["Worker"] : []),
      ...(buffer.preview ? ["Preview"] : []),
      ...(buffer.pinned ? ["Pinned"] : []),
      ...trailingTabStateLabels({
        agentLabels: props.agentPresenceForPath(buffer.rootId, buffer.path)?.labels ?? [],
        dirty: buffer.dirty,
        change: change(buffer),
      }),
      ...(props.mergingKey === buffer.key ? ["Merge in progress"] : []),
    ];
  };
  const openListRows = createMemo(() =>
    props.scope.stripOrder()
      .map((key) => {
        const buffer = state().byKey[key];
        if (!buffer) return null;
        return {
          key,
          buffer,
          agent: props.agentPresenceForPath(buffer.rootId, buffer.path),
          change: change(buffer),
          previousVersion: props.previousVersionForBuffer(buffer),
        };
      })
      .filter((row): row is NonNullable<typeof row> => row != null),
  );
  return (
    <FilesTabStripShell
      treeCollapsed={props.treeCollapsed}
      side={props.side}
      onToggleTree={props.onToggleTree}
      onAutoRestore={props.onAutoRestore}
    >
      <div class="den-files-tabs-wrap">
        <div
          class="den-files-tabs-fade den-files-tabs-fade--leading"
          classList={{
            "den-files-tabs-fade--visible": props.showLeadingFade,
          }}
          aria-hidden="true"
        />
        <div
          ref={(el) => props.onScrollerElement(el)}
          class="den-files-tabs-scroller"
          onWheel={(e) => {
            if (e.deltaY !== 0 && e.deltaX === 0) {
              e.currentTarget.scrollLeft += e.deltaY;
            }
          }}
        >
          <div
            ref={(el) => {
              tabsEl = el;
              props.onTabsElement(el);
              el.tabIndex = -1;
              const claim = {};
              registerFocusRegion("filesTabs", el, claim);
              el.addEventListener("focus", () => el.querySelector<HTMLElement>('[role="tab"][tabindex="0"]')?.focus());
              onCleanup(() => releaseFocusRegion("filesTabs", claim));
            }}
            class="den-files-tabs"
            role="tablist"
            aria-label="Open files"
            data-testid="files-tabs"
            onKeyDown={props.tabs.onTabListKeyDown}
          >
            <For each={props.scope.stripOrder()}>
              {(key) => {
                const buf = () => state().byKey[key];
                return (
                  <Show when={buf()} keyed>
                    {(b) => (
                      <FilesTab
                        buffer={b}
                        name={tabNames().get(key)}
                        stateLabels={stateLabelsFor(b)}
                        nameChange={() => change(b)}
                        active={props.scope.aimedKey() === key}
                        pending={props.scope.editorPresentationPending()}
                        focusable={(props.tabs.rovingTabKey() ?? props.scope.aimedKey()) === key}
                        dragging={props.drag.dragKey() === key}
                        dropBefore={props.drag.visibleDropAt()?.key === key && props.drag.visibleDropAt()?.after === false}
                        dropAfter={props.drag.visibleDropAt()?.key === key && props.drag.visibleDropAt()?.after === true}
                        previousVersion={props.previousVersionForBuffer(b)}
                        merging={props.mergingKey === key}
                        onPointerDown={props.drag.onTabPointerDown}
                        onActivate={props.tabs.activateRovingTab}
                        onPromote={(key) => promoteFilesBuffer(props.scope.projectId(), key)}
                        onContextMenu={props.onTabContextMenu}
                        onClose={props.requestCloseTab}
                      />
                    )}
                  </Show>
                );
              }}
            </For>
          </div>
        </div>
        <div
          class="den-files-tabs-fade den-files-tabs-fade--trailing"
          classList={{
            "den-files-tabs-fade--visible": props.showTrailingFade,
          }}
          aria-hidden="true"
        />
      </div>
      <Show when={props.hiddenTabCount > 0}>
        <button
          ref={overflowBtnEl}
          type="button"
          class="den-files-tabs-overflow"
          data-testid="files-tabs-overflow"
          aria-haspopup="dialog"
          aria-expanded={props.tabs.filesOpenListOpen()}
          aria-label={`Show all open files, ${props.hiddenTabCount} hidden`}
          onClick={props.tabs.toggleFilesOpenList}
        >
          <span
            class="den-files-tabs-overflow__chev"
            aria-hidden="true"
          >
            ›
          </span>
          <span class="den-files-tabs-overflow__count" style={{ "min-width": `${String(props.scope.stripOrder().length).length}ch`, "text-align": "end" }}>
            {props.hiddenTabCount}
          </span>
        </button>
      </Show>
      <FilesOpenList
        open={props.tabs.filesOpenListOpen()}
        rows={openListRows()}
        activeKey={props.scope.aimedKey()}
        anchorEl={openListAnchor()}
        onClose={() => props.tabs.setFilesOpenListOpen(false)}
        onActivate={(key) => {
          setFilesActiveBuffer(props.scope.projectId(), key);
        }}
        onCloseBuffer={(key) => props.requestCloseTab(key)}
        onRowContextMenu={(key, anchor) => props.onTabContextMenu(key, anchor)}
      />
    </FilesTabStripShell>
  );
}
