import { useResidentInteractive } from "../ui/resident-presence-context.tsx";
import { For, Show, createSignal, createMemo, createUniqueId, onCleanup, createEffect, on } from "solid-js";
import { AnchoredSurface } from "./primitives/AnchoredSurface.tsx";
import type { AnchoredSide } from "./primitives/AnchoredSurface.tsx";
import {
  captureSelectionLease,
  focusRetainingSelection,
  type SelectionLease,
} from "../platform/interaction/selection-lease.ts";
import { DEN_SCROLLPORT_AXIS_ATTR, DEN_SCROLLPORT_CLASS, DEN_SCROLLPORT_CONTENT_CLASS, DEN_SCROLLPORT_VIEWPORT_CLASS } from "../platform/scrolling/scrollport-frame-dom.ts";

type ContextMenuFields = {
  label: string;
  shortcut?: string;
  danger?: boolean;
  disabled?: boolean;
  checked?: boolean;
  kicker?: string;
  description?: string;
  /** Runs when hover or focus previews the action. */
  onHighlight?: () => void;
  testId?: string;
};

type ContextMenuAction = ContextMenuFields & {
  onSelect: () => void;
  separator?: never;
  header?: never;
  submenu?: never;
};

type ContextMenuDisabledItem = ContextMenuFields & {
  disabled: true;
  onSelect?: never;
  separator?: never;
  header?: never;
  submenu?: never;
};

type ContextMenuSeparator = Omit<ContextMenuFields, "label"> & {
  label?: never;
  separator: true;
  onSelect?: never;
  header?: never;
  submenu?: never;
};

type ContextMenuHeader = ContextMenuFields & {
  header: true;
  onSelect?: never;
  separator?: never;
  submenu?: never;
};

type ContextMenuEntry =
  | ContextMenuAction
  | ContextMenuDisabledItem
  | ContextMenuSeparator
  | ContextMenuHeader;

type ContextMenuSubmenu = ContextMenuFields & {
  submenu: readonly ContextMenuEntry[];
  onSelect?: never;
  separator?: never;
  header?: never;
};

export type ContextMenuItem = ContextMenuEntry | ContextMenuSubmenu;

export type ContextMenuAnchor = Element | { x: number; y: number };

type Props = {
  anchor: ContextMenuAnchor;
  items: readonly ContextMenuItem[];
  align?: "start" | "end";
  side?: AnchoredSide;
  gap?: number;
  label?: string;
  /** Virtual menu navigation preserves the field selection. */
  retainFocus?: boolean;
  onDismiss: () => void;
};

type SurfaceProps = Props & {
  autoFocus: boolean;
  depth: number;
  dismissTree: () => void;
  onEscape?: () => void;
  onReturnToParent?: () => void;
  surfaceRef?: (element: HTMLDivElement) => void;
  submenuLabel?: string;
};

function actionable(item: ContextMenuItem): boolean {
  return (
    item.disabled !== true &&
    item.separator !== true &&
    item.header !== true &&
    (item.submenu === undefined || item.submenu.length > 0)
  );
}

function firstEnabledItem(surface: HTMLElement | undefined): HTMLElement | null {
  return (
    surface?.querySelector<HTMLElement>(
      '[role="menuitem"]:not(:disabled),[role="menuitemcheckbox"]:not(:disabled)',
    ) ?? null
  );
}

/** Menu navigation plus bare modifier presses; every other key dismisses. */
const VIRTUAL_FOCUS_KEYS = new Set([
  "ArrowUp",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "Home",
  "End",
  "Enter",
  "Escape",
  "Shift",
  "Control",
  "Alt",
  "Meta",
]);

function ContextMenuSurface(props: SurfaceProps) {
  const items = createMemo(() => props.items);
  const interactive = useResidentInteractive();
  const [focusIdx, setFocusIdx] = createSignal(0);
  const [openSubmenuIdx, setOpenSubmenuIdx] = createSignal<number | null>(null);
  const [virtualFocus, setVirtualFocus] = createSignal(false);
  let menuEl: HTMLDivElement | undefined;
  let submenuEl: HTMLDivElement | undefined;
  let rootLease: SelectionLease | null = null;
  let returnFocus: HTMLElement | null = null;
  // Focus waits for placement so opening lays the menu out once.
  let focusOnPlacement = false;

  const enabledIndexes = () =>
    items()
      .map((item, i) => (actionable(item) ? i : -1))
      .filter((i) => i >= 0);

  const accessibleLabel = () => {
    const header = items().find(
      (item) => item.header && (item.kicker || item.description),
    );
    if (!header) return "Context menu";
    return [header.kicker, header.label, header.description]
      .filter(Boolean)
      .join(". ");
  };

  const hasCheckColumn = () =>
    items().some((item) => item.checked !== undefined);

  const select = (item: ContextMenuItem) => {
    if (item.disabled || item.separator || item.header) return;
    if (item.submenu !== undefined) return;
    item.onSelect();
    props.dismissTree();
  };

  const openSubmenu = (index: number, focusChild: boolean) => {
    const item = items()[index];
    if (!item?.submenu?.length || item.disabled) return;
    if (props.depth > 0) {
      throw new Error("ContextMenu supports one submenu level");
    }
    submenuEl = undefined;
    setOpenSubmenuIdx(index);
    if (focusChild) {
      queueMicrotask(() => {
        if (interactive()) firstEnabledItem(submenuEl)?.focus({ preventScroll: true });
      });
    }
  };

  const onKeyDown = (event: KeyboardEvent) => {
    const enabled = enabledIndexes();
    if (enabled.length === 0) return;
    const current = enabled.indexOf(focusIdx());
    const at = current < 0 ? 0 : current;
    const focusSlot = (slot: number) => {
      const next = enabled[slot];
      // The list can update while open.
      if (next === undefined) return;
      setFocusIdx(next);
      setOpenSubmenuIdx(null);
      if (virtualFocus()) return;
      const item = menuEl?.querySelector<HTMLButtonElement>(
        `[data-menu-index="${next}"]`,
      );
      if (item) focusRetainingSelection(item);
    };
    const item = items()[focusIdx()];
    if (event.key === "ArrowRight" && item?.submenu?.length) {
      event.preventDefault();
      event.stopPropagation();
      openSubmenu(focusIdx(), true);
    } else if (event.key === "ArrowLeft" && props.onReturnToParent) {
      event.preventDefault();
      event.stopPropagation();
      props.onReturnToParent();
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      focusSlot((at + 1) % enabled.length);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      focusSlot((at - 1 + enabled.length) % enabled.length);
    } else if (event.key === "Home") {
      event.preventDefault();
      focusSlot(0);
    } else if (event.key === "End") {
      event.preventDefault();
      focusSlot(enabled.length - 1);
    } else if (event.key === "Enter") {
      if (item?.submenu?.length) {
        event.preventDefault();
        openSubmenu(focusIdx(), true);
      } else if (item && !item.disabled) {
        event.preventDefault();
        select(item);
      }
    }
  };

  createEffect(on(() => interactive(), (active) => {
    if (!active) return;
    const el = menuEl;
    if (!el) return;
    const first = enabledIndexes()[0] ?? 0;
    setFocusIdx(first);
    if (!props.autoFocus) return;

    const lease = captureSelectionLease();
    if (props.depth === 0) {
      rootLease = lease;
      const active = document.activeElement;
      returnFocus = active instanceof HTMLElement && active !== document.body ? active : null;
    }
    // A submenu row needs a real focus target to hand its child.
    const hasSubmenu = items().some((item) => item.submenu !== undefined);
    // A pointer menu over any selection — a field's or the reader's — navigates
    // virtually, so the selection it acts on never moves.
    if (props.retainFocus && lease.held && !hasSubmenu) {
      setVirtualFocus(true);
      const onDocumentKey = (event: KeyboardEvent) => {
        if (event.defaultPrevented) return;
        onKeyDown(event);
        if (event.defaultPrevented || VIRTUAL_FOCUS_KEYS.has(event.key)) return;
        props.dismissTree();
      };
      document.addEventListener("keydown", onDocumentKey, true);
      onCleanup(() => document.removeEventListener("keydown", onDocumentKey, true));
      return;
    }

    focusOnPlacement = true;
    onCleanup(() => { focusOnPlacement = false; });
  }));

  const focusFirstItem = (surface: HTMLElement) => {
    if (!focusOnPlacement) return;
    focusOnPlacement = false;
    if (!interactive()) return;
    const item = surface.querySelector<HTMLButtonElement>(
      `[data-menu-index="${focusIdx()}"]`,
    );
    if (item) focusRetainingSelection(item);
  };

  // Restore only when dismissal leaves focus empty.
  onCleanup(() => {
    const lease = rootLease;
    const target = returnFocus;
    if (!lease || !target) return;
    queueMicrotask(() => {
      const active = document.activeElement;
      if (active && active !== document.body && active.isConnected) return;
      if (lease.target) lease.restore();
      else if (target.isConnected) focusRetainingSelection(target, { preventScroll: true });
    });
  });

  return (
    <AnchoredSurface
      ref={(element) => {
        // The placed surface is the scrollport frame.
        element.setAttribute(DEN_SCROLLPORT_AXIS_ATTR, "y");
        menuEl = element;
        props.surfaceRef?.(element);
      }}
      anchor={() => props.anchor}
      preferredSide={props.side ?? "bottom"}
      align={props.align ?? "start"}
      gap={props.gap ?? 0}
      height="viewport"
      overflow="hidden"
      dismissOnScroll
      onDismiss={props.onDismiss}
      onEscape={props.onEscape}
      onPositioned={focusFirstItem}
      class={`${DEN_SCROLLPORT_CLASS} den-menu-surface den-context-menu`}
      role="menu"
      menuFocus="managed"
      ariaLabel={props.submenuLabel ?? props.label ?? accessibleLabel()}
      tabIndex={-1}
      testId="context-menu"
      chrome
      onKeyDown={onKeyDown}
      onMouseDown={(e) => {
        e.preventDefault();
      }}
      onContextMenu={(e) => {
        e.preventDefault();
      }}
    >
      <div class={DEN_SCROLLPORT_VIEWPORT_CLASS}><div class={`${DEN_SCROLLPORT_CONTENT_CLASS} den-context-menu__content`}>
        <For each={items()}>
          {(item, index) => {
            const descriptionId = createUniqueId();
            const autoSep =
              item.danger === true &&
              !item.separator &&
              index() > 0 &&
              items()[index() - 1]?.danger !== true &&
              items()[index() - 1]?.separator !== true;
            return (
              <>
                <Show when={item.separator === true}>
                  <div
                    class="den-context-menu__sep"
                    role="separator"
                    data-testid={item.testId}
                  />
                </Show>
                <Show when={item.header === true && item.separator !== true}>
                  <div
                    class="den-context-menu__header"
                    classList={{
                      "den-context-menu__header--descriptive":
                        Boolean(item.kicker || item.description),
                    }}
                    role="presentation"
                    data-testid={item.testId}
                  >
                    <Show when={item.kicker}>
                      <span class="den-context-menu__header-kicker">
                        {item.kicker}
                      </span>
                    </Show>
                    <span class="den-context-menu__header-label">{item.label}</span>
                    <Show when={item.description}>
                      <span class="den-context-menu__header-description">
                        {item.description}
                      </span>
                    </Show>
                  </div>
                </Show>
                <Show when={item.separator !== true && item.header !== true}>
                  <Show when={autoSep}>
                    <div class="den-context-menu__sep" role="separator" />
                  </Show>
                  {(() => {
                    let trigger: HTMLButtonElement | undefined;
                    const hasSubmenu = () => (item.submenu?.length ?? 0) > 0;
                    const disabled = () =>
                      item.disabled === true ||
                      (item.submenu !== undefined && item.submenu.length === 0);
                    const submenuState = () => {
                      const items = item.submenu;
                      if (
                        openSubmenuIdx() !== index() ||
                        !trigger ||
                        !items?.length
                      ) {
                        return null;
                      }
                      return { anchor: trigger, items };
                    };
                    return (
                      <>
                        <button
                          ref={(element) => {
                            trigger = element;
                          }}
                          type="button"
                          role={
                            item.checked === undefined
                              ? "menuitem"
                              : "menuitemcheckbox"
                          }
                          aria-checked={
                            item.checked === undefined
                              ? undefined
                              : item.checked === true
                          }
                          aria-label={item.label}
                          aria-describedby={item.description ? descriptionId : undefined}
                          aria-haspopup={hasSubmenu() ? "menu" : undefined}
                          aria-expanded={
                            hasSubmenu()
                              ? openSubmenuIdx() === index()
                              : undefined
                          }
                          class="den-context-menu__item"
                          classList={{
                            "den-context-menu__item--danger":
                              item.danger === true,
                            "den-context-menu__item--disabled":
                              item.disabled === true,
                            "den-context-menu__item--checkable":
                              hasCheckColumn(),
                            "den-context-menu__item--virtual-focus":
                              virtualFocus() && focusIdx() === index() && !disabled(),
                          }}
                          disabled={disabled()}
                          aria-disabled={disabled() ? "true" : undefined}
                          data-menu-index={index()}
                          data-testid={item.testId}
                          onClick={() => {
                            if (hasSubmenu()) openSubmenu(index(), true);
                            else select(item);
                          }}
                          onFocus={() => {
                            setFocusIdx(index());
                            if (openSubmenuIdx() !== index()) {
                              setOpenSubmenuIdx(null);
                            }
                            item.onHighlight?.();
                          }}
                          onMouseEnter={() => {
                            setFocusIdx(index());
                            if (hasSubmenu()) openSubmenu(index(), false);
                            else setOpenSubmenuIdx(null);
                            item.onHighlight?.();
                          }}
                        >
                          <Show when={hasCheckColumn()}>
                            <span
                              class="den-context-menu__check"
                              classList={{
                                "den-context-menu__check--on":
                                  item.checked === true,
                              }}
                              aria-hidden="true"
                            >
                              ✓
                            </span>
                          </Show>
                          <span class="den-context-menu__text">
                            <span class="den-context-menu__label">{item.label}</span>
                            <Show when={item.description}>
                              <span id={descriptionId} class="den-context-menu__description">{item.description}</span>
                            </Show>
                          </span>
                          <Show when={item.shortcut}>
                            <span class="den-context-menu__shortcut">
                              {item.shortcut}
                            </span>
                          </Show>
                          <Show when={hasSubmenu()}>
                            <span
                              class="den-context-menu__submenu-arrow"
                              aria-hidden="true"
                            >
                              ›
                            </span>
                          </Show>
                        </button>
                        <Show when={submenuState()} keyed>
                          {(submenu) => (
                            <ContextMenuSurface
                              anchor={submenu.anchor}
                              items={submenu.items}
                              side="right"
                              align="start"
                              gap={2}
                              autoFocus={false}
                              depth={props.depth + 1}
                              dismissTree={props.dismissTree}
                              onDismiss={() => setOpenSubmenuIdx(null)}
                              onEscape={() => {
                                setOpenSubmenuIdx(null);
                                queueMicrotask(() =>
                                  trigger?.focus({ preventScroll: true }),
                                );
                              }}
                              onReturnToParent={() => {
                                setOpenSubmenuIdx(null);
                                queueMicrotask(() =>
                                  trigger?.focus({ preventScroll: true }),
                                );
                              }}
                              surfaceRef={(element) => {
                                submenuEl = element;
                              }}
                              submenuLabel={`${item.label} submenu`}
                            />
                          )}
                        </Show>
                      </>
                    );
                  })()}
                </Show>
              </>
            );
          }}
        </For>
      </div></div>
    </AnchoredSurface>
  );
}

export function ContextMenu(props: Props) {
  return (
    <ContextMenuSurface
      {...props}
      autoFocus
      depth={0}
      dismissTree={props.onDismiss}
    />
  );
}
