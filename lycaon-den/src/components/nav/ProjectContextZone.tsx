import { windowMenuItems } from "../window-menu-items.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import {
  DragDropProvider,
  DragDropSensors,
  SortableProvider,
  closestCenter,
  createSortable,
  maybeTransformStyle,
  useDragDropContext,
  type Id,
} from "@thisbeyond/solid-dnd";
import { For, Show, createMemo, createSignal, onMount, type JSX } from "solid-js";
import { CONTEXT_NAV_CATALOG, type ContextNavItemId } from "../../../shared/app-state-types.ts";
import {
  contextNavVisiblePref,
  reorderContextNavVisible,
  saveContextNavVisible,
} from "../../settings/editor/context-nav-prefs.ts";
import { setSplitFocusRegion } from "../../shell/layout-store.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import type { ItemWindowView } from "../../platform/windows/item-windows.ts";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../ContextMenu.tsx";
import { STAGE_REGISTRY, stageDefinition } from "../stage/stage-registry.tsx";
import { isContextNavItemId } from "../../settings/editor/context-nav-prefs.ts";
import { NavSelectionDot, navRow } from "./NavSelectionDot.tsx";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";

type Props = {
  projectId?: string | null;
  /** A split is on screen — entries open into its stage column. */
  splitLive?: boolean;
  /** Stage ids with one or more live peer windows. */
  detachedStages?: ReadonlySet<ContextNavItemId>;
  /** Host-assigned view slots for each Context item. */
  windowViewNumbers?: ReadonlyMap<ContextNavItemId, readonly number[]>;
  /** Every live peer view for this project, in host-assigned slot order. */
  projectViews?: readonly ItemWindowView[];
  /** Pop the stage into its own window (desktop only). */
  onOpenInNewWindow?: (stageId: ContextNavItemId) => void;
  /** Bring a detached window forward. */
  onRaiseWindow?: (stageId: ContextNavItemId, viewNumber?: number) => void;
  /** Close exactly one host-registered Context peer view. */
  onCloseWindow?: (stageId: ContextNavItemId, viewNumber: number) => void;
  onFocusProjectView?: (label: string) => void;
  onCloseProjectView?: (label: string) => void;
  /** Open / raise a Context stage — one path for every catalog id. */
  onOpenStage: (stageId: ContextNavItemId) => void;
  /** Whether the stage is the active Context entry in this shell. */
  isStageActive: (stageId: ContextNavItemId) => boolean;
  /** Feature gate from the stage registry. */
  isStageAvailable: (stageId: ContextNavItemId) => boolean;
};

function isContextNavId(id: Id): id is ContextNavItemId {
  return typeof id === "string" && isContextNavItemId(id);
}

type RowSharedProps = {
  id: ContextNavItemId;
  customizing: boolean;
  entryActive: boolean;
  detached: boolean;
  onOpen: () => void;
  onHide: () => void;
  onContextMenu: (e: MouseEvent) => void;
};

/** Pointer-driven sortable row. */
function ContextSortableRow(props: RowSharedProps) {
  const sortable = createSortable(props.id);
  const dnd = useDragDropContext();

  return (
    <li
      ref={sortable.ref}
      class="project-context-zone__row"
      classList={{
        "project-context-zone__row--dragging": sortable.isActiveDraggable,
        "project-context-zone__row--sorting":
          dnd?.[0].active.draggableId != null,
      }}
      style={maybeTransformStyle(sortable.transform)}
      data-testid={`project-context-row-${props.id}`}
    >
      <ContextNavRowBody
        {...props}
        gripActivators={sortable.dragActivators as JSX.HTMLAttributes<HTMLButtonElement>}
      />
    </li>
  );
}

function ContextStaticRow(props: RowSharedProps & { rowIndex: number }) {
  return (
    <li
      class="project-context-zone__row"
      data-testid={`project-context-row-${props.id}`}
      {...navRow(props.rowIndex)}
    >
      <ContextNavRowBody {...props} />
    </li>
  );
}

function ContextNavRowBody(
  props: RowSharedProps & {
    gripActivators?: JSX.HTMLAttributes<HTMLButtonElement>;
  },
) {
  const meta = () => stageDefinition(props.id);
  const ariaLabel = () =>
    props.detached
      ? `${meta().label} — open in its own window, bring to front`
      : meta().label;

  // Ignore non-primary buttons during reorder.
  const gripProps = (): JSX.HTMLAttributes<HTMLButtonElement> => {
    const base = props.gripActivators ?? {};
    const prior = base.onPointerDown;
    return {
      ...base,
      onPointerDown: (e) => {
        if (e.button !== 0) {
          e.stopPropagation();
          return;
        }
        if (typeof prior === "function") {
          (prior as (ev: PointerEvent) => void)(e);
        }
      },
    };
  };

  onMount(() => {
    if (props.id === "files") requestFirstTimeTip("files-ai-editor");
  });

  return (
    <>
      <div class="project-context-zone__row-main">
        <Show when={props.customizing}>
          <button
            type="button"
            class="project-context-zone__grip"
            data-testid={`project-context-grip-${props.id}`}
            aria-label={`Reorder ${meta().label}`}
            data-tip="Drag to reorder"
            {...gripProps()}
          >
            <GripIcon />
          </button>
        </Show>
        <button
          type="button"
          class="project-context-zone__entry"
          classList={{
            "project-context-zone__entry--active":
              !props.customizing && props.entryActive && !props.detached,
            "project-context-zone__entry--detached":
              !props.customizing && props.detached,
          }}
          data-testid={meta().navTestId}
          data-first-time-tip-anchor={
            props.id === "files" ? "files-ai-editor" : undefined
          }
          data-detached={props.detached ? "true" : undefined}
          aria-label={ariaLabel()}
          aria-current={
            !props.customizing && props.entryActive && !props.detached
              ? "page"
              : undefined
          }
          onClick={() => {
            props.onOpen();
          }}
          onContextMenu={(e) => {
            e.preventDefault();
            e.stopPropagation();
            props.onContextMenu(e);
          }}
          onKeyDown={(e) => {
            if (e.key !== "ContextMenu" && !(e.shiftKey && e.key === "F10")) {
              return;
            }
            e.preventDefault();
            const rect = e.currentTarget.getBoundingClientRect();
            props.onContextMenu(
              new MouseEvent("contextmenu", {
                clientX: rect.left + rect.width / 2,
                clientY: rect.top + rect.height / 2,
                bubbles: true,
              }),
            );
          }}
          onPointerDown={(e) => {
            if (e.button !== 0) e.stopPropagation();
          }}
        >
          <span class="project-context-zone__icon" aria-hidden="true">
            {meta().icon()}
          </span>
          <span class="project-context-zone__entry-label">{meta().label}</span>
          <Show when={props.detached && !props.customizing}>
            <span
              class="project-context-zone__window-glyph"
              aria-hidden="true"
              data-testid={`project-context-detached-${props.id}`}
            />
          </Show>
        </button>
        <Show when={props.customizing}>
          <button
            type="button"
            class="project-context-zone__action den-inset-icon-btn"
            data-testid={`project-context-hide-${props.id}`}
            aria-label={`Hide ${meta().label}`}
            onClick={() => props.onHide()}
          >
            −
          </button>
        </Show>
      </div>
    </>
  );
}

/** Pinned project context — device-wide show/hide + order. */
export function ProjectContextZone(props: Props) {
  const [customizing, setCustomizing] = createSignal(false);
  const [menu, setMenu] = createSignal<{
    stageId: ContextNavItemId;
    anchor: ContextMenuAnchor;
  } | null>(null);
  const [projectViewsMenu, setProjectViewsMenu] =
    createSignal<ContextMenuAnchor | null>(null);

  const isDetached = (id: ContextNavItemId) =>
    props.detachedStages?.has(id) === true;
  const viewNumbersFor = (id: ContextNavItemId) =>
    props.windowViewNumbers?.get(id) ?? [];

  const pinnedVisible = createMemo(() =>
    contextNavVisiblePref().filter((id) => props.isStageAvailable(id)),
  );

  const addable = createMemo(() => {
    const visible = new Set(contextNavVisiblePref());
    return CONTEXT_NAV_CATALOG.filter(
      (id) => !visible.has(id) && props.isStageAvailable(id),
    );
  });

  const disabledFeatureItems = createMemo(() =>
    CONTEXT_NAV_CATALOG.filter((id) => !props.isStageAvailable(id)),
  );

  /** Active context stage shown in this column. */
  const activeIndex = createMemo(() =>
    pinnedVisible().findIndex((id) => props.isStageActive(id) && !isDetached(id)),
  );

  const openEntry = (id: ContextNavItemId) => {
    if (customizing()) return;
    // Re-click focuses the column; onOpenStage still claims chrome.
    if (props.splitLive === true && props.isStageActive(id)) {
      setSplitFocusRegion("stage");
      focusRegion("context");
    }
    props.onOpenStage(id);
  };

  const menuItems = (id: ContextNavItemId): ContextMenuItem[] => {
    return windowMenuItems({
      views: viewNumbersFor(id).map((number) => ({ id: number, number })), testIdPrefix: "context-nav",
      open: props.onOpenInNewWindow ? () => props.onOpenInNewWindow?.(id) : undefined,
      focus: props.onRaiseWindow ? (number) => props.onRaiseWindow?.(id, number) : undefined,
      close: props.onCloseWindow ? (number) => props.onCloseWindow?.(id, number) : undefined,
    });
  };

  const openRowMenu = (id: ContextNavItemId, e: MouseEvent) => {
    if (customizing() || !isTauriRuntime()) return;
    if (menuItems(id).length === 0) return;
    setMenu({ stageId: id, anchor: { x: e.clientX, y: e.clientY } });
  };

  const projectViewMenuItems = (): ContextMenuItem[] => {
    return windowMenuItems({
      views: (props.projectViews ?? []).map((view) => ({ id: view.label, number: view.viewNumber, title: view.title })),
      testIdPrefix: "project-context", focus: props.onFocusProjectView, close: props.onCloseProjectView,
    });
  };

  const hideItem = async (id: ContextNavItemId) => {
    await saveContextNavVisible(
      contextNavVisiblePref().filter((x) => x !== id),
    );
  };

  const showItem = async (id: ContextNavItemId) => {
    if (!props.isStageAvailable(id)) return;
    if (contextNavVisiblePref().includes(id)) return;
    await saveContextNavVisible([...contextNavVisiblePref(), id]);
  };

  const commitReorder = async (
    fromId: ContextNavItemId,
    toId: ContextNavItemId,
  ) => {
    const next = reorderContextNavVisible(
      contextNavVisiblePref(),
      fromId,
      toId,
    );
    await saveContextNavVisible(next);
  };

  const rowProps = (id: ContextNavItemId): RowSharedProps => ({
    id,
    customizing: customizing(),
    entryActive: props.isStageActive(id) && !isDetached(id),
    detached: isDetached(id),
    onOpen: () => openEntry(id),
    onHide: () => void hideItem(id),
    onContextMenu: (e) => openRowMenu(id, e),
  });

  return (
    <section
      class="project-context-zone"
      classList={{ "project-context-zone--customizing": customizing() }}
      data-testid="project-context-zone"
      aria-label="Project context"
    >
      <div class="project-context-zone__header">
        <h2 class="project-context-zone__heading">Context</h2>
        <Show
          when={
            (props.projectViews?.length ?? 0) > 0 &&
            (props.onFocusProjectView != null || props.onCloseProjectView != null)
          }
        >
          <button
            type="button"
            class="project-context-zone__views"
            data-testid="project-context-views"
            aria-label={`${props.projectViews?.length} open views`}
            aria-haspopup="menu"
            aria-expanded={projectViewsMenu() != null}
            onClick={(event) =>
              setProjectViewsMenu({ x: event.clientX, y: event.clientY })
            }
          >
            Views {props.projectViews?.length}
          </button>
        </Show>
      </div>

      <Show
        when={customizing()}
        fallback={
          <ul
            class="project-context-zone__list"
            data-testid="project-context-visible"
          >
            <NavSelectionDot index={activeIndex()} />
            <For each={pinnedVisible()}>
              {(id, index) => (
                <ContextStaticRow {...rowProps(id)} rowIndex={index()} />
              )}
            </For>
            <li class="project-context-zone__row">
              <button
                type="button"
                class="project-context-zone__more"
                data-testid="project-context-customize"
                aria-label={
                  addable().length > 0
                    ? `${addable().length} more Context ${
                        addable().length === 1 ? "item" : "items"
                      }. Customize context.`
                    : "Customize context"
                }
                onClick={() => setCustomizing(true)}
              >
                {addable().length > 0
                  ? `${addable().length} more`
                  : "Customize"}
              </button>
            </li>
          </ul>
        }
      >
        <DragDropProvider
          collisionDetector={closestCenter}
          onDragEnd={({ draggable, droppable }) => {
            if (!draggable || !droppable) return;
            if (!isContextNavId(draggable.id) || !isContextNavId(droppable.id)) {
              return;
            }
            if (draggable.id === droppable.id) return;
            void commitReorder(draggable.id, droppable.id);
          }}
        >
          <DragDropSensors>
            <SortableProvider ids={pinnedVisible()}>
              <ul
                class="project-context-zone__list"
                data-testid="project-context-visible"
              >
                <For each={pinnedVisible()}>
                  {(id) => <ContextSortableRow {...rowProps(id)} />}
                </For>
              </ul>
            </SortableProvider>
          </DragDropSensors>
        </DragDropProvider>
      </Show>

      <Show when={customizing()}>
        <div
          class="project-context-zone__add"
          data-testid="project-context-add"
        >
          <h3 class="project-context-zone__add-heading">Add to context</h3>
          <ul class="project-context-zone__list project-context-zone__list--add">
            <For each={addable()}>
              {(id) => {
                const meta = STAGE_REGISTRY[id];
                return (
                  <li class="project-context-zone__row">
                    <div class="project-context-zone__row-main">
                      <button
                        type="button"
                        class="project-context-zone__entry project-context-zone__entry--add"
                        data-testid={`project-context-show-${id}`}
                        onClick={() => void showItem(id)}
                      >
                        <span
                          class="project-context-zone__icon"
                          aria-hidden="true"
                        >
                          {meta.icon()}
                        </span>
                        <span class="project-context-zone__entry-label">
                          {meta.label}
                        </span>
                      </button>
                      <button
                        type="button"
                        class="project-context-zone__action project-context-zone__action--add den-inset-icon-btn"
                        aria-label={`Show ${meta.label}`}
                        onClick={() => void showItem(id)}
                      >
                        +
                      </button>
                    </div>
                  </li>
                );
              }}
            </For>
            <For each={disabledFeatureItems()}>
              {(id) => {
                const meta = STAGE_REGISTRY[id];
                return (
                  <li class="project-context-zone__row">
                    <div class="project-context-zone__row-main">
                      <button
                        type="button"
                        class="project-context-zone__entry project-context-zone__entry--feature-off"
                        data-testid={`project-context-disabled-${id}`}
                        disabled
                        data-tip="Turn on in Settings"
                      >
                        <span
                          class="project-context-zone__icon"
                          aria-hidden="true"
                        >
                          {meta.icon()}
                        </span>
                        <span class="project-context-zone__entry-label">
                          {meta.label}
                        </span>
                        <span class="project-context-zone__off-badge">Off</span>
                      </button>
                    </div>
                  </li>
                );
              }}
            </For>
          </ul>
          <Show
            when={addable().length === 0 && disabledFeatureItems().length === 0}
          >
            <p class="project-context-zone__add-empty">All items are shown</p>
          </Show>
        </div>
        <button
          type="button"
          class="project-context-zone__done"
          data-testid="project-context-done"
          onClick={() => setCustomizing(false)}
        >
          Done
        </button>
      </Show>

      <Show when={menu()} keyed>
        {(m) => (
          <ContextMenu
            anchor={m.anchor}
            items={menuItems(m.stageId)}
            onDismiss={() => setMenu(null)}
          />
        )}
      </Show>
      <Show when={projectViewsMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            onDismiss={() => setProjectViewsMenu(null)}
            items={projectViewMenuItems()}
          />
        )}
      </Show>
    </section>
  );
}

function GripIcon() {
  return (
    <ThemeIcon slot="grip" size={10} />
  );
}
