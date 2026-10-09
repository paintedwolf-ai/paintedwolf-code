import { For, Show } from "solid-js";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { editorTreeVisibleLevelsPref } from "../../settings/editor/editor-prefs.ts";
import { TreeContext, type TreeOps, type DropAssessment, type PendingMove } from "./files-tree-context.ts";
import type { StickySlot, StickyFold } from "./files-tree-sticky.ts";
import type { DisplayRow } from "./files-tree-display-model.ts";
import type { RenderedRow } from "./files-tree-virtual-scroll.ts";
import { FilesTreeStickyOverlay } from "./FilesTreeStickyOverlay.tsx";
import { StickySlotBox, SwitchDisplayRow } from "./FilesTreeRows.tsx";
type FilesTreeSurfaceProps = {
  ops: TreeOps;
  bindNavEl(element: HTMLElement): void;
  bindFilterEl(element: HTMLInputElement): void;
  onKeyDown(event: KeyboardEvent): void;
  forwardStickyWheel(event: WheelEvent): void;
  stickyHost(): HTMLElement | undefined;
  stickyHeight(): number;
  stickyKeys(): readonly string[];
  stickySlots(): readonly StickySlot[];
  stickyTops(): readonly number[];
  physicalRowOffset(offset: number): number;
  stickyFold(): StickyFold;
  extentHeight(): number;
  paintOffset(): number;
  renderedRows(): ReadonlyMap<string, RenderedRow<DisplayRow>>;
  showFilterEmpty(): boolean;
  dragGhost(): { x: number; y: number; label: string } | null;
  dropAssessment(): DropAssessment | null;
  movePending(): PendingMove | null;
  rootLabel(root: string): string;
};
export function FilesTreeSurface(props: FilesTreeSurfaceProps) {
  return (
    <>
      <Show when={props.ops.props.filterOpen}>
        <div class="den-files-tree-pane-header">
          <input
            ref={props.bindFilterEl}
            type="search"
            class="den-files-tree__filter"
            data-testid="files-tree-filter"
            data-files-ctx="no-menu"
            placeholder="Filter files"
            value={props.ops.props.filterQuery ?? ""}
            onInput={(e) => props.ops.props.onFilterQueryChange?.(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                props.ops.props.onFilterQueryChange?.("");
                props.ops.props.onFilterClose?.();
                e.preventDefault();
                e.stopPropagation();
              }
            }}
          />
        </div>
      </Show>
      <TreeContext.Provider value={props.ops}>
        <FilesTreeStickyOverlay
          host={props.stickyHost()}
          height={props.stickyHeight()}
          onKeyDown={props.onKeyDown}
          onWheel={props.forwardStickyWheel}
        >
          <For each={props.stickyKeys()}>
            {key => <StickySlotBox slotKey={key} slots={props.stickySlots} tops={props.stickyTops} />}
          </For>
        </FilesTreeStickyOverlay>
      <nav
        ref={props.bindNavEl}
        class="den-files-tree"
        data-testid="files-tree"
        data-files-ctx="tree-background"
        aria-label="Project files"
        data-fold-levels={props.stickyFold().levels}
        data-fold-floor={props.stickyFold().floor}
        onKeyDown={props.onKeyDown}
      >
          <div
            class="den-files-tree-virtual"
            style={{ height: `${props.extentHeight()}px` }}
          >
            <div class="den-files-tree-virtual__inner" style={{ transform: props.paintOffset() ? `translateY(${props.paintOffset()}px)` : undefined }}>
              <For each={[...props.renderedRows().keys()]}>
                {key => (
                  <ShowLatest when={props.renderedRows().get(key)}>
                    {item => (
                      <div
                        class="den-files-tree-virtual__row"
                        classList={{
                          "den-files-tree-virtual__row--lead":
                            item().position.index === 0 && editorTreeVisibleLevelsPref() !== 0 &&
                            props.stickySlots().length === 0 && props.paintOffset() === 0,
                        }}
                        data-display-key={item().row.key}
                        data-row-kind={item().row.kind}
                        style={{
                          height: `${item().position.size}px`,
                          transform: `translateY(${props.physicalRowOffset(item().position.start)}px)`,
                        }}
                      >
                        <SwitchDisplayRow row={item().row} />
                      </div>
                    )}
                  </ShowLatest>
                )}
              </For>
            </div>
          </div>
          <Show when={props.showFilterEmpty()}>
            <p
              class="den-files-tree__hint"
              data-testid="files-tree-filter-empty"
            >
              No files match "{(props.ops.props.filterQuery ?? "").trim()}"
            </p>
          </Show>
      </nav>
      </TreeContext.Provider>
      <Show when={props.dragGhost()} keyed>
        {(g) => (
          <div
            class="den-files-tree__drag-ghost"
            classList={{
              "den-files-tree__drag-ghost--invalid": props.dropAssessment()?.valid === false,
            }}
            style={{ left: `${g.x}px`, top: `${g.y}px` }}
            role="status"
          >
            <strong class="den-files-tree__drag-ghost-title">{g.label}</strong>
            <Show when={props.dropAssessment()} keyed>
              {(assessment) => (
                <span>
                  {assessment.valid ? `Move to ${assessment.destination}` : assessment.reason}
                </span>
              )}
            </Show>
          </div>
        )}
      </Show>
      <Show when={props.movePending()} keyed>
        {(move) => (
          <div class="den-files-tree__move-status" role="status">
            Moving {move.source.name} to {move.target.dir === "." ? `@${props.rootLabel(move.target.rootId)}` : move.target.dir}…
          </div>
        )}
      </Show>
    </>
  );
}
