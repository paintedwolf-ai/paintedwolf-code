import { createEffect, createMemo, on, onCleanup, untrack } from "solid-js";
import { glideScrollLeft } from "../../platform/scrolling/scrollport-reveal.ts";
import { editorRevealInTreePref } from "../../settings/editor/editor-prefs.ts";
import { fileTabKind } from "../tabs/files-tab-kind.ts";
import { isShellLayoutBusy, onShellLayoutSettled } from "../../shell/shell-layout-busy.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import type { FilesScope } from "../components/files-scope.ts";

type TabMeasurementDependencies = Pick<FilesScope, "state" | "aimedKey" | "stripOrder" | "tabsElement" | "scrollerElement"> & {
  previousVersionForBuffer: (buffer: FileBuffer) => boolean;
  tabStripMountEpoch: () => number;
  setRovingTabKey: (key: FileBufferKey | null) => void;
  setHiddenTabCount: (count: number) => void;
  setShowLeadingFade: (shown: boolean) => void;
  setShowTrailingFade: (shown: boolean) => void;
};

export function createFilesTabMeasurements({ state, aimedKey, stripOrder, previousVersionForBuffer,
  tabsElement, scrollerElement, tabStripMountEpoch, setRovingTabKey,
  setHiddenTabCount, setShowLeadingFade, setShowTrailingFade }: TabMeasurementDependencies) {
  let activeTabScrollFrame: number | undefined;
  let cancelTabGlide: (() => void) | undefined;
  /** Defers tab-strip positioning until after the active-tab swap. */
  const scrollActiveTabIntoView = () => {
    if (activeTabScrollFrame !== undefined) {
      cancelAnimationFrame(activeTabScrollFrame);
    }
    activeTabScrollFrame = requestAnimationFrame(() => {
      activeTabScrollFrame = undefined;
      const key = untrack(aimedKey);
      const tabsEl = tabsElement();
      const scroller = scrollerElement();
      if (!key || !tabsEl || !scroller) return;
      for (const tab of tabsEl.querySelectorAll<HTMLElement>(".den-files-tab")) {
        if (tab.dataset.key !== key) continue;
        const tabBox = tab.getBoundingClientRect();
        const box = scroller.getBoundingClientRect();
        const delta = tabBox.left < box.left
          ? tabBox.left - box.left
          : tabBox.right > box.right ? Math.min(tabBox.left - box.left, tabBox.right - box.right) : 0;
        if (delta === 0) break;
        cancelTabGlide?.();
        cancelTabGlide = glideScrollLeft(scroller, scroller.scrollLeft + delta);
        break;
      }
    });
  };
  let revealFallbackTimer: ReturnType<typeof setTimeout> | undefined;
  const syncScrollActiveTabIntoView = () => {
    clearTimeout(revealFallbackTimer);
    revealFallbackTimer = undefined;
    scrollActiveTabIntoView();
  };
  onCleanup(() => {
    if (activeTabScrollFrame !== undefined) {
      cancelAnimationFrame(activeTabScrollFrame);
    }
    cancelTabGlide?.();
    clearTimeout(revealFallbackTimer);
  });

  createEffect(() => {
    const key = aimedKey();
    setRovingTabKey(key);
    if (editorRevealInTreePref()) {
      clearTimeout(revealFallbackTimer);
      revealFallbackTimer = setTimeout(() => {
        scrollActiveTabIntoView();
      }, 120);
    } else {
      scrollActiveTabIntoView();
    }
  });

  createEffect(on(() => {
    const key = aimedKey();
    const buffer = key ? state().byKey[key] : null;
    return buffer ? fileTabKind(buffer, previousVersionForBuffer(buffer))?.icon : undefined;
  }, scrollActiveTabIntoView, { defer: true }));

  /** Invalidates measurements when tab content changes. */
  const tabStripSignature = createMemo(() => {
    const s = state();
    return stripOrder()
      .map((key) => {
        const b = s.byKey[key];
        if (!b) return key;
        const icon = fileTabKind(b, previousVersionForBuffer(b))?.icon ?? "";
        return `${key}:${b.name}:${icon}:${b.dirty ? 1 : 0}:${b.preview ? 1 : 0}:${
          b.pinned ? 1 : 0
        }`;
      })
      .join("|");
  });

  // Reading all bounds before attribute writes avoids repeated layout.
  const recountHiddenTabs = () => {
    const scroller = scrollerElement();
    const tablist = tabsElement();
    if (!scroller || !tablist || !tablist.isConnected) return;
    if (isShellLayoutBusy()) return;
    const tabs = [...tablist.querySelectorAll<HTMLElement>(".den-files-tab")];
    const rootRect = scroller.getBoundingClientRect();
    const hiddenFlags = tabs.map((tab) => {
      const rect = tab.getBoundingClientRect();
      return rect.left < rootRect.left - 1 || rect.right > rootRect.right + 1;
    });
    const scrollLeft = scroller.scrollLeft;
    const maxScroll = scroller.scrollWidth - scroller.clientWidth;

    let hidden = 0;
    tabs.forEach((tab, index) => {
      const isHidden = hiddenFlags[index] === true;
      const next = isHidden ? "true" : "false";
      // Unchanged attribute writes also invalidate style.
      if (tab.dataset.filesHidden !== next) tab.dataset.filesHidden = next;
      if (isHidden) hidden += 1;
    });
    setHiddenTabCount(hidden);
    setShowLeadingFade(scrollLeft > 1);
    setShowTrailingFade(maxScroll > 1 && scrollLeft < maxScroll - 1);
  };

  // Strip measurements share one animation frame.
  let recountFrame = 0;
  const scheduleRecount = () => {
    if (recountFrame !== 0) return;
    recountFrame = requestAnimationFrame(() => {
      recountFrame = 0;
      recountHiddenTabs();
    });
  };
  onCleanup(() => {
    if (recountFrame !== 0) cancelAnimationFrame(recountFrame);
  });

  // Observers follow strip mounts rather than tab selections.
  createEffect(() => {
    tabStripMountEpoch();
    const scroller = scrollerElement();
    const tablist = tabsElement();
    if (!scroller || !tablist) return;
    scroller.addEventListener("scroll", scheduleRecount, { passive: true });
    const ro =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver(scheduleRecount);
    ro?.observe(tablist);
    ro?.observe(scroller);
    const mo =
      typeof MutationObserver === "undefined"
        ? null
        : new MutationObserver(scheduleRecount);
    mo?.observe(tablist, { childList: true });
    const stopLayoutSettle = onShellLayoutSettled(scheduleRecount);
    scheduleRecount();
    onCleanup(() => {
      scroller.removeEventListener("scroll", scheduleRecount);
      ro?.disconnect();
      mo?.disconnect();
      stopLayoutSettle();
    });
  });

  // Labels and status marks can resize tabs without adding elements.
  createEffect(on(tabStripSignature, scheduleRecount));

  return { scrollActiveTabIntoView: syncScrollActiveTabIntoView };
}
