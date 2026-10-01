import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  on,
  onCleanup,
  untrack,
  type Component,
  type JSX,
} from "solid-js";
import { LAYOUT_BAND_SCALES, bindLayoutBand } from "../../layout/layout-bands.ts";
import { Dynamic } from "solid-js/web";
import {
  TAB_PANEL_TRANSITION_MS,
  type TabId,
  nextOpen,
} from "../chatview/tabs.ts";
import { dragWindowOnMove } from "../../platform/windows/window-drag-gesture.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { type FirstTimeTipId } from "../../first-time-tips/first-time-tips-catalog.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";

export type TabSpec = {
  id: TabId;
  label: string;
  /** Optional first-time guidance anchor for this tab. */
  firstTimeTip?: FirstTimeTipId;
  /** Glyph used by compact chips. */
  icon?: JSX.Element;
  badge?: JSX.Element;
  /** Status dot on the chip. */
  active?: boolean;
  /** Gray the unselected chip; the panel still opens fully. */
  empty?: boolean;
  panel: Component;
};

const CONTENT_FADE_MS = 110;
/** Matches `--den-ease` in global.css. */
const EASE = "cubic-bezier(0.4, 0, 0.2, 1)";

function keepEditorFocus(e: MouseEvent) {
  e.preventDefault();
}

type TabBarProps = {
  tabs: TabSpec[];
  /** Controlled open tab. */
  open?: TabId | null;
  onOpenChange?: (next: TabId | null) => void;
  /** Collapse the panel while retaining selection. */
  retracted?: boolean;
  /** Reports the panel's target height for the short-transcript runway. */
  onPanelHeightChange?: (heightPx: number) => void;
  /** Rows below the overlay panel; an opening tab pushes them down. */
  dock?: JSX.Element;
  leading?: JSX.Element;
  trailing?: JSX.Element;
};

export function TabBar(props: TabBarProps) {
  const [openInternal, setOpenInternal] = createSignal<TabId | null>(null);
  const open = () =>
    props.onOpenChange ? (props.open ?? null) : openInternal();
  const setOpen = (next: TabId | null) => {
    if (props.onOpenChange) props.onOpenChange(next);
    else setOpenInternal(next);
  };

  // A caller may rebuild its spec array on every read, so index it once.
  const tabById = createMemo(
    () => new Map(props.tabs.map((tab) => [tab.id, tab])),
  );
  // Stable ids preserve keyed chip identity.
  const tabIds = createMemo(() => [...tabById().keys()]);
  const specFor = (id: TabId) => tabById().get(id);
  const selectedSpec = () => {
    const id = open();
    return id ? specFor(id) : undefined;
  };
  const isOpen = () => selectedSpec() != null;

  // Mounted content survives the closing animation.
  const [mounted, setMounted] = createSignal<TabId | null>(
    isOpen() ? open() : null,
  );
  const [contentVisible, setContentVisible] = createSignal<boolean>(isOpen());
  const showContent = () => mounted() != null;

  const railCompact = () => open() == null && mounted() == null;

  // A window drag consumes its trailing chip click.
  let draggedDuringPress = false;
  const onRailPointerDown = (e: PointerEvent) => {
    draggedDuringPress = false;
    dragWindowOnMove(e, () => {
      draggedDuringPress = true;
    });
  };
  const onChipClick = (tab: TabSpec) => {
    if (draggedDuringPress) {
      draggedDuringPress = false;
      return;
    }
    setOpen(nextOpen(open(), tab.id));
  };

  let clipEl: HTMLDivElement | undefined;
  let heightAnim: Animation | undefined;
  let panelWashEl: HTMLDivElement | undefined;
  let panelWashAnim: Animation | undefined;
  let dockEl: HTMLDivElement | undefined;
  let dockAnim: Animation | undefined;
  let pendingTimer: ReturnType<typeof setTimeout> | undefined;
  let fadeListener: { panel: Element; onEnd: (e: Event) => void } | undefined;
  let contentRo: ResizeObserver | undefined;
  let contentResizeFrame: number | undefined;

  const clearPending = () => {
    if (pendingTimer) clearTimeout(pendingTimer);
    pendingTimer = undefined;
    if (fadeListener) {
      fadeListener.panel.removeEventListener("transitionend", fadeListener.onEnd);
      fadeListener = undefined;
    }
  };

  const measureAfterLayout = (fn: () => void) => {
    requestAnimationFrame(() => requestAnimationFrame(fn));
  };

  const readClipRenderedHeight = (clip: HTMLDivElement): number =>
    Math.round(clip.getBoundingClientRect().height);

  const snapClipHeight = (clip: HTMLDivElement): number => {
    const prev = clip.style.height;
    clip.style.height = "auto";
    const h = readClipRenderedHeight(clip);
    clip.style.height = prev;
    return h;
  };

  const freezeClipHeight = (clip: HTMLDivElement): number => {
    const h = snapClipHeight(clip);
    clip.style.height = `${h}px`;
    return h;
  };

  const readExpandedTargetHeight = (clip: HTMLDivElement): number => {
    const panel = clip.querySelector<HTMLElement>(".tabs__panel");
    if (!panel) return snapClipHeight(clip);
    const panelHeight = panel.getBoundingClientRect().height;
    if (panelHeight <= 0) return snapClipHeight(clip);
    const cs = getComputedStyle(clip);
    const extra =
      (Number.parseFloat(cs.borderTopWidth) || 0) +
      (Number.parseFloat(cs.borderBottomWidth) || 0) +
      (Number.parseFloat(cs.paddingTop) || 0) +
      (Number.parseFloat(cs.paddingBottom) || 0);
    return Math.round(panelHeight + extra);
  };

  const panelExpandedHeight = (): number => {
    const clip = clipEl;
    if (!clip) return 0;
    return readExpandedTargetHeight(clip);
  };

  const stopExpandedContentWatch = () => {
    contentRo?.disconnect();
    contentRo = undefined;
    if (contentResizeFrame !== undefined) {
      cancelAnimationFrame(contentResizeFrame);
      contentResizeFrame = undefined;
    }
  };

  const scheduleExpandedContentResize = () => {
    if (contentResizeFrame !== undefined) return;
    contentResizeFrame = requestAnimationFrame(() => {
      contentResizeFrame = undefined;
      if (!clipEl || !isOpen() || (props.retracted ?? false)) return;
      const to = panelTargetHeight(true);
      if (to <= 0) return;
      const from = readClipRenderedHeight(clipEl);
      animateHeight(from, to, "auto");
    });
  };

  const bindExpandedContentWatch = () => {
    stopExpandedContentWatch();
    const clip = clipEl;
    const panel = clip?.querySelector<HTMLElement>(".tabs__panel");
    if (!clip || !panel) return;
    contentRo = new ResizeObserver(scheduleExpandedContentResize);
    contentRo.observe(panel);
  };

  const panelTargetHeight = (expanded: boolean): number =>
    expanded ? panelExpandedHeight() : 0;

  // The dock follows the panel edge without changing the header height.
  const dockOffset = (px: number) => (px > 0 ? `translateY(${px}px)` : "");

  const cancelOverlayExtentAnims = () => {
    panelWashAnim?.cancel();
    panelWashAnim = undefined;
    dockAnim?.cancel();
    dockAnim = undefined;
  };

  const settleOverlayExtent = (px: number) => {
    if (panelWashEl) panelWashEl.style.height = `${px}px`;
    if (dockEl) dockEl.style.transform = dockOffset(px);
  };

  const animateOverlayExtent = (from: number, to: number) => {
    const opts: KeyframeAnimationOptions = {
      duration: TAB_PANEL_TRANSITION_MS,
      easing: EASE,
      fill: "forwards",
    };
    if (panelWashEl && typeof panelWashEl.animate === "function") {
      panelWashAnim = panelWashEl.animate(
        [{ height: `${from}px` }, { height: `${to}px` }],
        opts,
      );
    }
    if (dockEl && typeof dockEl.animate === "function") {
      dockAnim = dockEl.animate(
        [
          { transform: `translateY(${from}px)` },
          { transform: `translateY(${to}px)` },
        ],
        opts,
      );
    }
  };

  const animateHeight = (
    from: number,
    to: number,
    settleTo: string,
    onDone?: () => void,
  ) => {
    const clip = clipEl;
    if (!clip) {
      onDone?.();
      return;
    }
    heightAnim?.cancel();
    heightAnim = undefined;
    cancelOverlayExtentAnims();
    props.onPanelHeightChange?.(to);
    const finish = () => {
      clip.style.height = settleTo;
      settleOverlayExtent(to);
      onDone?.();
    };
    if (
      from === to ||
      prefersReducedMotion() ||
      typeof clip.animate !== "function"
    ) {
      finish();
      return;
    }
    clip.style.height = `${from}px`;
    settleOverlayExtent(from);
    animateOverlayExtent(from, to);
    const anim = clip.animate(
      [{ height: `${from}px` }, { height: `${to}px` }],
      { duration: TAB_PANEL_TRANSITION_MS, easing: EASE, fill: "forwards" },
    );
    heightAnim = anim;
    anim.onfinish = () => {
      if (heightAnim !== anim) return;
      heightAnim = undefined;
      cancelOverlayExtentAnims();
      try {
        anim.commitStyles();
      } catch {
        clip.style.height = `${to}px`;
      }
      anim.cancel();
      finish();
    };
  };

  const afterContentFadeOut = (panel: Element | null | undefined, fn: () => void) => {
    if (!panel || prefersReducedMotion()) {
      fn();
      return;
    }
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      if (fadeListener) {
        fadeListener.panel.removeEventListener("transitionend", fadeListener.onEnd);
        fadeListener = undefined;
      }
      if (pendingTimer) clearTimeout(pendingTimer);
      pendingTimer = undefined;
      fn();
    };
    const onEnd = (e: Event) => {
      if (e.target !== panel) return;
      if (!(e instanceof TransitionEvent) || e.propertyName !== "opacity") return;
      finish();
    };
    fadeListener = { panel, onEnd };
    panel.addEventListener("transitionend", onEnd);
    pendingTimer = setTimeout(finish, CONTENT_FADE_MS + 32);
  };

  const runRetractSlide = (retracted: boolean) => {
    if (!isOpen() || !clipEl || !showContent()) return;
    if (retracted) stopExpandedContentWatch();
    const from = readClipRenderedHeight(clipEl);
    const to = panelTargetHeight(!retracted);
    const settleTo = retracted ? "0px" : "auto";
    animateHeight(from, to, settleTo);
    if (!retracted) bindExpandedContentWatch();
  };

  const runTransition = () => {
    clearPending();
    const tab = open();
    const cur = mounted();

    if (!isOpen()) {
      // Keep content mounted through the closing slide.
      if (cur == null) return;
      stopExpandedContentWatch();
      setContentVisible(false);
      const from = clipEl ? snapClipHeight(clipEl) : 0;
      animateHeight(from, 0, "0px");
      pendingTimer = setTimeout(() => {
        pendingTimer = undefined;
        setMounted(null);
      }, TAB_PANEL_TRANSITION_MS);
      return;
    }

    if (cur == null) {
      // Measure after the mounted content finishes layout.
      setMounted(tab);
      setContentVisible(true);
      measureAfterLayout(() => {
        if (!clipEl) return;
        const expanded = !(props.retracted ?? false);
        const to = panelTargetHeight(expanded);
        animateHeight(0, to, expanded ? "auto" : "0px");
        if (expanded) bindExpandedContentWatch();
      });
      return;
    }

    if (cur === tab) {
      setContentVisible(true);
      if (props.retracted ?? false) stopExpandedContentWatch();
      else bindExpandedContentWatch();
      return;
    }

    // Swap content between fade phases.
    setContentVisible(false);
    const panel = clipEl?.querySelector(".tabs__panel");
    afterContentFadeOut(panel, () => {
      if (!clipEl) return;
      const from = freezeClipHeight(clipEl);
      setMounted(tab);
      setContentVisible(true);
      measureAfterLayout(() => {
        if (!clipEl) return;
        const expanded = !(props.retracted ?? false);
        const to = panelTargetHeight(expanded);
        animateHeight(from, to, expanded ? "auto" : "0px");
        if (expanded) bindExpandedContentWatch();
      });
    });
  };

  createEffect(on(open, runTransition));

  createEffect(
    on(
      () => props.retracted ?? false,
      (retracted, prev) => {
        if (prev === undefined) return;
        if (!isOpen()) return;
        runRetractSlide(retracted);
      },
    ),
  );

  onCleanup(() => {
    clearPending();
    stopExpandedContentWatch();
    heightAnim?.cancel();
    cancelOverlayExtentAnims();
  });

  return (
    <Show when={tabIds().length > 0 || props.leading != null || props.trailing != null}>
      <div class="tabs" data-testid="tabs">
        <div
          class="tabs__rail"
          classList={{
            "tabs__rail--compact": railCompact(),
          }}
          {...chromeProps()}
        >
          <Show when={props.leading}>
            <div
              class="tabs__rail-leading"
              onPointerDown={(e) => e.stopPropagation()}
            >
              {props.leading}
            </div>
          </Show>
          <div
            class="tabs__panel-wash"
            ref={(el) => {
              panelWashEl = el;
              el.style.height = "0px";
            }}
            aria-hidden="true"
          >
            <Show when={mounted() != null}>
              <div
                class="tabs__panel-fade"
                classList={{ "tabs__panel-fade--hidden": props.retracted === true }}
              />
            </Show>
          </div>
          <div class="tabs__strip">
            {/* The panel anchors to the chips. */}
            <div class="tabs__row">
              <div
                class="tabs__chips"
                onPointerDown={onRailPointerDown}
              >
                <For each={tabIds()}>
                  {(id) => {
                    const tab = createMemo(() => specFor(id));
                    createEffect(() => {
                      const tip = tab()?.firstTimeTip;
                      if (tip) requestFirstTimeTip(tip);
                    });
                    const chipShown = () =>
                      open() === id || (mounted() === id && open() == null);
                    const empty = () => tab()?.empty === true;
                    return (
                      <button
                        type="button"
                        class="tabs__chip"
                        classList={{
                          "tabs__chip--shown": chipShown(),
                          "tabs__chip--empty": !chipShown() && empty(),
                          "tabs__chip--active": tab()?.active === true,
                          "tabs__chip--with-icon": tab()?.icon != null,
                        }}
                        aria-expanded={open() === id}
                        aria-controls="tabs-panel"
                        aria-label={tab()?.label}
                        data-tip={tab()?.icon ? tab()?.label : undefined}
                        data-tip-when-clipped={
                          tab()?.icon ? ".tabs__chip-label" : undefined
                        }
                        data-testid={`tab-${id}`}
                        data-first-time-tip-anchor={tab()?.firstTimeTip}
                        onMouseDown={keepEditorFocus}
                        onClick={() => {
                          const spec = tab();
                          if (spec) onChipClick(spec);
                        }}
                      >
                        <Show when={tab()?.active === true}>
                          <span class="tabs__chip-dot" aria-hidden="true" />
                        </Show>
                        <Show when={tab()?.icon}>
                          <span class="tabs__chip-icon" aria-hidden="true">
                            {tab()?.icon}
                          </span>
                        </Show>
                        <span class="tabs__chip-label">{tab()?.label}</span>
                        <Show when={tab()?.badge != null}>
                          <span class="tabs__chip-badge">{tab()?.badge}</span>
                        </Show>
                      </button>
                    );
                  }}
                </For>
              </div>
              <div class="tabs__panel-shell">
                <Show when={mounted() != null}>
                  <div
                    class="tabs__panel-clip"
                    data-testid="tabs-clip"
                    ref={(el) => {
                      clipEl = el;
                      el.style.height = "0px";
                    }}
                  >
                    <Show when={showContent() ? mounted() : null} keyed>
                      {(id) => {
                        // Untracking preserves the mounted panel when tab metadata changes.
                        const Panel = untrack(() => specFor(id)?.panel);
                        return (
                          <div
                            ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.tabsPanel)}
                            id="tabs-panel"
                            class="tabs__panel"
                            classList={{
                              "tabs__panel--hidden": !contentVisible(),
                            }}
                            role="region"
                            aria-label={specFor(id)?.label ?? "Panel"}
                            data-testid="tabs-panel"
                            data-tab={id}
                          >
                            <Dynamic component={Panel} />
                          </div>
                        );
                      }}
                    </Show>
                  </div>
                </Show>
              </div>
            </div>
          </div>
          <Show when={props.trailing}>
            <div
              class="tabs__rail-trailing"
              onPointerDown={(e) => e.stopPropagation()}
            >
              {props.trailing}
            </div>
          </Show>
        </div>
        {/* The dock spans the rail beneath the side slots, not just the chip strip. */}
        <div
          class="tabs__dock"
          ref={(el) => {
            dockEl = el;
          }}
        >
          {props.dock}
        </div>
      </div>
    </Show>
  );
}
