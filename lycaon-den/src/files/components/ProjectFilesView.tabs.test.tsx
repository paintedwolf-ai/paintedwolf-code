import "../../test/document-outbox-fixture.ts";
import { PROJECT, ROOTS, loadedBuffer, resetProjectFilesViewTest, filesViewTestFakes, PROJECT_SOURCE_IDENTITY } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, fireEvent } from "@solidjs/testing-library";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { openFilesBuffer, resetProjectFilesForTests, applyFilesBufferLoad, setFilesBufferPinned, applyFilesBufferDraft } from "../documents/project-files-buffers.ts";
import { updateFileTabDropTarget } from "../../platform/windows/item-windows.ts";
import { beginShellLayoutBusy, endShellLayoutBusy, resetShellLayoutBusyForTests } from "../../shell/shell-layout-busy.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { readSourceText } from "../../test/stylesheet-source.ts";

const { fileTabDragListeners } = filesViewTestFakes();

describe("ProjectFilesView stage chrome", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("measures and publishes the tab drop target once after layout settles", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    loadedBuffer();
    const { container } = render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));
    await waitFor(() => {
      expect(
        screen.getByTestId("project-files-stage-boundary").dataset.boot,
      ).toBe("ready");
    });
    const scroller = await waitFor(() => {
      const found = container.querySelector<HTMLElement>(
        ".den-files-tabs-scroller",
      );
      if (!found) throw new Error("tab scroller did not mount");
      return found;
    });
    await waitFor(() => expect(fileTabDragListeners.size).toBe(1));
    await Promise.resolve();
    const getRect = vi.spyOn(scroller, "getBoundingClientRect").mockReturnValue({
      x: 100,
      y: 50,
      left: 100,
      top: 50,
      right: 700,
      bottom: 90,
      width: 600,
      height: 40,
      toJSON: () => ({}),
    });
    const updateTarget = vi.mocked(updateFileTabDropTarget);
    updateTarget.mockClear();

    resetShellLayoutBusyForTests();
    beginShellLayoutBusy();
    window.dispatchEvent(new Event("resize"));
    expect(getRect).not.toHaveBeenCalled();
    expect(updateTarget).not.toHaveBeenCalled();

    endShellLayoutBusy();
    await waitFor(() => expect(updateTarget).toHaveBeenCalledTimes(1));
    expect(getRect).toHaveBeenCalled();

    window.dispatchEvent(new Event("resize"));
    expect(updateTarget).toHaveBeenCalledTimes(1);
  });

  it("strip geometry: one row at 20 tabs, clamps, preview/pin, roving tabindex", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    for (let i = 0;i < 20;i++) {
      const key = openFilesBuffer(PROJECT, {
        intent: i === 19 ? "transient" : "permanent",
        rootId: "r1",
        rootLabel: "repo",
        path: `src/file-${String(i).padStart(2, "0")}-very-long-name.ts`,
      });
      applyFilesBufferLoad(PROJECT, key, {
        ...PROJECT_SOURCE_IDENTITY,
        file_id: "",
        version_id: "",
        path: `src/file-${String(i).padStart(2, "0")}-very-long-name.ts`,
        content: "x",
        over_limit: false,
        writable: true,
        binary: false,
        size_bytes: 1,
        sha256: "s",
      });
      if (i === 0) setFilesBufferPinned(PROJECT, key, true);
    }

    const OriginalIO = globalThis.IntersectionObserver;
    globalThis.IntersectionObserver = class {
      constructor(private cb: IntersectionObserverCallback) { }
      observe() {
        this.cb([], this as unknown as IntersectionObserver);
      }
      unobserve() { }
      disconnect() { }
      takeRecords() {
        return [];
      }
      root = null;
      rootMargin = "";
      thresholds = [];
    } as unknown as typeof IntersectionObserver;

    const { unmount } = render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
      />
    ));

    const tablist = screen.getByTestId("files-tabs");
    expect(getComputedStyle(tablist).flexWrap || "nowrap").not.toBe("wrap");
    const tabs = screen.getAllByTestId("files-tab");
    expect(tabs.length).toBe(20);
    expect(document.querySelector(".den-files-tab--preview")).toBeTruthy();
    expect(screen.getByTestId("files-tab-pin")).toBeTruthy();
    expect(document.querySelector(".den-files-tab__name-end")?.textContent).toBe(
      ".ts",
    );

    const tabbable = tabs.filter((t) => t.getAttribute("tabindex") === "0");
    expect(tabbable).toHaveLength(1);
    fireEvent.keyDown(tablist, { key: "ArrowLeft" });
    const after = screen
      .getAllByTestId("files-tab")
      .filter((t) => t.getAttribute("tabindex") === "0");
    expect(after).toHaveLength(1);

    const previewKey = projectFilesState(PROJECT).order.find(
      (key) => projectFilesState(PROJECT).byKey[key]?.preview,
    )!;
    const previewTab = document.querySelector(".den-files-tab--preview [role=tab]");
    fireEvent.dblClick(previewTab!);
    expect(projectFilesState(PROJECT).byKey[previewKey]?.preview).toBe(false);
    expect(document.querySelector(".den-files-tab--preview")).toBeNull();
    openFilesBuffer(PROJECT, {
      intent: "transient", rootId: "r1", rootLabel: "repo", path: "next.ts",
    });
    expect(projectFilesState(PROJECT).byKey[previewKey]).toBeTruthy();

    const { join } = await import("node:path");
    const css = readSourceText(
      join(import.meta.dirname, "../../files-domain.css"),
      "utf8",
    );
    expect(css).toMatch(/\.den-files-tab\s*\{[\s\S]*?min-width:\s*7\.4286rem/);
    expect(css).toMatch(/\.den-files-tab\s*\{[\s\S]*?max-width:\s*13\.1429rem/);
    expect(css).toMatch(/flex-wrap:\s*nowrap/);

    globalThis.IntersectionObserver = OriginalIO;
    unmount();
  });

  it("toggles the open-files list from the hidden-tab count", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const first = loadedBuffer({ path: "src/first.ts" });
    const second = loadedBuffer({ path: "src/second.ts" });

    const { unmount } = render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
      />
    ));

    await waitFor(() => {
      expect(
        screen.getByTestId("project-files-stage-boundary").dataset.boot,
      ).toBe("ready");
    });

    const scroller = document.querySelector<HTMLElement>(
      ".den-files-tabs-scroller",
    )!;
    const rect = (left: number, width: number) =>
      ({
        bottom: 24,
        height: 24,
        left,
        right: left + width,
        top: 0,
        width,
        x: left,
        y: 0,
        toJSON: () => ({}),
      }) as DOMRect;
    scroller.getBoundingClientRect = () => rect(0, 100);
    for (const tab of document.querySelectorAll<HTMLElement>(
      ".den-files-tab",
    )) {
      tab.getBoundingClientRect = () =>
        tab.dataset.key === first.key
          ? rect(0, 80)
          : tab.dataset.key === second.key
            ? rect(80, 80)
            : rect(0, 0);
    }
    fireEvent.scroll(scroller);

    const overflow = await screen.findByTestId("files-tabs-overflow");
    const tabs = [...document.querySelectorAll<HTMLElement>(".den-files-tab")];
    await waitFor(() => {
      expect(tabs[0]?.dataset.filesHidden).toBe("false");
      expect(tabs[1]?.dataset.filesHidden).toBe("true");
    });
    fireEvent.click(overflow);
    expect(screen.getByTestId("files-open-list")).toBeTruthy();
    expect(overflow.getAttribute("aria-expanded")).toBe("true");

    fireEvent.click(overflow);
    expect(screen.queryByTestId("files-open-list")).toBeNull();
    expect(overflow.getAttribute("aria-expanded")).toBe("false");

    unmount();
  });

  it("bulk close asks once with multi-file dialog; cancel is atomic", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const keys: string[] = [];
    for (const path of ["a.ts", "b.ts", "c.ts"]) {
      const key = openFilesBuffer(PROJECT, {
        intent: "permanent",
        rootId: "r1",
        rootLabel: "repo",
        path,
      });
      applyFilesBufferLoad(PROJECT, key, {
        ...PROJECT_SOURCE_IDENTITY,
        file_id: "",
        version_id: "",
        path,
        content: "base",
        over_limit: false,
        writable: true,
        binary: false,
        size_bytes: 4,
        sha256: "sha",
      });
      applyFilesBufferDraft(PROJECT, key, "dirty");
      keys.push(key);
    }

    const { unmount } = render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
      />
    ));

    const tab = screen.getAllByTestId("files-tab")[0]!;
    fireEvent.contextMenu(tab.closest(".den-files-tab")!, {
      clientX: 10,
      clientY: 10,
    });
    fireEvent.click(await screen.findByTestId("files-ctx-tab-close-tabs"));
    const closeAll = await screen.findByTestId("files-ctx-tab-close-all");
    fireEvent.click(closeAll);

    expect(screen.getByTestId("editor-unsaved-dialog")).toBeTruthy();
    expect(screen.getByTestId("editor-unsaved-bulk-body")).toBeTruthy();
    fireEvent.click(screen.getByTestId("editor-unsaved-cancel"));
    expect(projectFilesState(PROJECT).order).toHaveLength(3);
    expect(screen.queryByTestId("editor-unsaved-dialog")).toBeNull();
    unmount();
  });
});
