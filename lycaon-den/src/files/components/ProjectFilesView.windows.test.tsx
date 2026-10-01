import "../../test/document-outbox-fixture.ts";
import { PROJECT, ROOTS, loadedBuffer, resetProjectFilesViewTest, filesViewTestFakes, waitForFilesStageReady } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { fileBufferKey } from "./project-files-model.ts";
import { resetAppStateSnapshotForTests, setAppStateSnapshot } from "../../store/app-state-snapshot.ts";

const { fileTabDragListeners } = filesViewTestFakes();

describe("ProjectFilesView stage chrome", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("does not restore workspace tabs in a file-addressed peer", () => {
    resetProjectFilesForTests();
    setAppStateSnapshot({
      version: 1,
      recents: [],
      filesHotExit: {
        byProject: {
          [PROJECT]: {
            buffers: [
              {
                rootId: "r1",
                path: "src/other.ts",
                rootLabel: "repo",
                kind: "text",
              },
            ],
          },
        },
      },
    });
    const appStore = createAppStore();
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        restoreHotExit={false}
      />
    ));

    expect(projectFilesState(PROJECT).order).toEqual([]);
    resetAppStateSnapshotForTests();
  });

  it("shows pull-off feedback, cancels it on reentry, and moves the tab to a window at the drop", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer({ path: "src/pulled-off.ts" });
    const onOpenInNewWindow = vi.fn();
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        onOpenInNewWindow={onOpenInNewWindow}
      />
    ));
    await waitForFilesStageReady();
    const tab = screen.getByTestId("files-tab").closest(".den-files-tab")!;

    tab.dispatchEvent(
      new MouseEvent("pointerdown", {
        button: 0,
        clientX: 40,
        clientY: 4,
        bubbles: true,
      }),
    );
    window.dispatchEvent(
      new MouseEvent("pointermove", { clientX: 45, clientY: 60 }),
    );
    expect(screen.getByTestId("files-tab-pull-preview").textContent).toContain(
      "pulled-off.ts",
    );

    window.dispatchEvent(
      new MouseEvent("pointermove", { clientX: 0, clientY: 0 }),
    );
    expect(screen.queryByTestId("files-tab-pull-preview")).toBeNull();

    window.dispatchEvent(
      new MouseEvent("pointermove", { clientX: 50, clientY: 60 }),
    );
    window.dispatchEvent(
      new MouseEvent("pointerup", { screenX: 700, screenY: 420 }),
    );
    window.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    expect(onOpenInNewWindow).toHaveBeenCalledWith(buffer.key, {
      x: 700,
      y: 420,
    });
    expect(screen.queryByTestId("files-tab-pull-preview")).toBeNull();
    await waitFor(() => {
      expect(projectFilesState(PROJECT).byKey[buffer.key]).toBeUndefined();
    });
  });

  it("hands a pull-off to a native destination window and finalizes it on release", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer({ path: "src/native-drag.ts" });
    const onOpenInNewWindow = vi.fn();
    const onFileTabMovedOut = vi.fn();
    const fileTabWindowDrag = {
      begin: vi.fn(async () => "file:native-drag:1"),
      move: vi.fn(async () => true),
      finish: vi.fn(async () => true),
      cancel: vi.fn(async () => true),
    };
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        onOpenInNewWindow={onOpenInNewWindow}
        fileTabWindowDrag={fileTabWindowDrag}
        onFileTabMovedOut={onFileTabMovedOut}
      />
    ));
    await waitForFilesStageReady();
    const tab = screen.getByTestId("files-tab").closest(".den-files-tab")!;

    tab.dispatchEvent(
      new MouseEvent("pointerdown", {
        button: 0,
        clientX: 40,
        clientY: 4,
        bubbles: true,
      }),
    );
    window.dispatchEvent(
      new MouseEvent("pointermove", {
        clientX: 50,
        clientY: 4,
        screenX: 680,
        screenY: 390,
      }),
    );
    await waitFor(() => {
      expect(fileTabWindowDrag.begin).toHaveBeenCalledWith(buffer.key, {
        x: 680,
        y: 390,
      });
      expect(fileTabWindowDrag.move).toHaveBeenCalled();
    });

    window.dispatchEvent(
      new MouseEvent("pointerup", { screenX: 700, screenY: 420 }),
    );
    window.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    await waitFor(() => {
      expect(fileTabWindowDrag.finish).toHaveBeenCalledWith(
        "file:native-drag:1",
        { x: 700, y: 420 },
      );
      expect(projectFilesState(PROJECT).byKey[buffer.key]).toBeUndefined();
      expect(onFileTabMovedOut).toHaveBeenCalledWith(buffer.key);
    });
    expect(fileTabWindowDrag.cancel).not.toHaveBeenCalled();
    expect(onOpenInNewWindow).not.toHaveBeenCalled();
  });

  it("previews and accepts one tab dragged into this window's visible Files strip", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const existing = loadedBuffer({ path: "src/existing.ts" });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
      />
    ));
    await waitFor(() => expect(fileTabDragListeners.size).toBe(1));
    const event = {
      dragLabel: "file:incoming:1",
      projectId: PROJECT,
      rootId: "r1",
      path: "src/incoming.ts",
      title: "incoming.ts",
      positionX: window.screenX,
      positionY: window.screenY,
    };

    for (const listener of fileTabDragListeners) {
      listener({ ...event, phase: "hover" });
    }
    await waitFor(() =>
      expect(
        screen
          .getByTestId("files-tab")
          .closest(".den-files-tab")
          ?.classList.contains("den-files-tab--drop-before"),
      ).toBe(true),
    );

    for (const listener of fileTabDragListeners) {
      listener({ ...event, phase: "drop" });
    }
    const incomingKey = fileBufferKey("r1", "src/incoming.ts");
    await waitFor(() => {
      expect(projectFilesState(PROJECT).order).toEqual([
        incomingKey,
        existing.key,
      ]);
      expect(projectFilesState(PROJECT).activeKey).toBe(incomingKey);
    });
  });

  it("closes a provisional native destination when the tab reenters the strip", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer({
      path: "src/native-cancel.ts",
      overLimit: true,
    });
    const fileTabWindowDrag = {
      begin: vi.fn(async () => "file:native-cancel:1"),
      move: vi.fn(async () => true),
      finish: vi.fn(async () => true),
      cancel: vi.fn(async () => true),
    };
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        fileTabWindowDrag={fileTabWindowDrag}
      />
    ));
    await waitForFilesStageReady();
    const tab = screen.getByTestId("files-tab").closest(".den-files-tab")!;

    tab.dispatchEvent(
      new MouseEvent("pointerdown", {
        button: 0,
        clientX: 40,
        clientY: 4,
        bubbles: true,
      }),
    );
    window.dispatchEvent(
      new MouseEvent("pointermove", {
        clientX: 50,
        clientY: 60,
        screenX: 680,
        screenY: 390,
      }),
    );
    await waitFor(() => expect(fileTabWindowDrag.move).toHaveBeenCalled());
    window.dispatchEvent(
      new MouseEvent("pointermove", { clientX: 0, clientY: 0 }),
    );

    await waitFor(() => {
      expect(fileTabWindowDrag.cancel).toHaveBeenCalledWith(
        "file:native-cancel:1",
      );
    });
    expect(fileTabWindowDrag.finish).not.toHaveBeenCalled();
    expect(projectFilesState(PROJECT).byKey[buffer.key]).toBeDefined();
    window.dispatchEvent(new MouseEvent("pointerup"));
    window.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });

  it("keeps the source tab when a pulled-off window cannot be created", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer({ path: "src/stays-put.ts" });
    const onOpenInNewWindow = vi.fn(() => Promise.reject(new Error("no window")));
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        onOpenInNewWindow={onOpenInNewWindow}
      />
    ));
    await waitForFilesStageReady();
    const tab = screen.getByTestId("files-tab").closest(".den-files-tab")!;

    tab.dispatchEvent(
      new MouseEvent("pointerdown", {
        button: 0,
        clientX: 40,
        clientY: 4,
        bubbles: true,
      }),
    );
    window.dispatchEvent(
      new MouseEvent("pointermove", { clientX: 45, clientY: 60 }),
    );
    window.dispatchEvent(new MouseEvent("pointerup"));
    window.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    await waitFor(() => expect(onOpenInNewWindow).toHaveBeenCalledOnce());
    expect(projectFilesState(PROJECT).byKey[buffer.key]).toBeDefined();
  });
});
