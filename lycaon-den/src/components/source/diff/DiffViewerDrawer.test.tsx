import { diffViewerFixture } from "../../../test/source-reader-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { DiffViewerDrawer } from "./DiffViewerDrawer.tsx";
import { resetDiffViewerForTests } from "../../../platform/navigation/in-app-diff.ts";
import { syncDisplayPrefsFromSnapshot } from "../../../settings/appearance/display-prefs.ts";
import { setAppStateSnapshot } from "../../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";

vi.mock("../../../platform/runtime.ts", async (original) => ({
  ...await original<typeof import("../../../platform/runtime.ts")>(),
  isTauriRuntime: () => true, tauriPlatform: () => "macos",
}));
vi.mock("@tauri-apps/api/window", () => ({ getCurrentWindow: () => ({ label: "main" }) }));
vi.mock("../../../platform/persistence/app-state.ts", async (original) => ({
  ...await original<typeof import("../../../platform/persistence/app-state.ts")>(),
  patchAppState: vi.fn(async (_patch: unknown, optimistic: unknown) => optimistic),
}));

const openInExternalEditor = vi.hoisted(() =>
  vi.fn(async () => ({ status: "opened" })),
);
const revealInFileManager = vi.hoisted(() =>
  vi.fn(async () => ({ status: "revealed" })),
);
const addToChat = vi.hoisted(() =>
  vi.fn(async (..._args: unknown[]) => ({ ok: true })),
);

vi.mock("../../../platform/navigation/external-source.ts", () => ({
  openInExternalEditor: (...args: unknown[]) =>
    openInExternalEditor(...(args as Parameters<typeof openInExternalEditor>)),
}));

vi.mock("../../../platform/files/reveal-in-file-manager.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../../platform/files/reveal-in-file-manager.ts")>();
  return {
    ...actual,
    revealInFileManager: (...args: Parameters<typeof revealInFileManager>) =>
      revealInFileManager(...args),
  };
});

vi.mock("../../../chat/composer/add-to-chat.ts", () => ({
  addToChat: (ref: unknown, options?: unknown) =>
    options === undefined ? addToChat(ref) : addToChat(ref, options),
}));

describe("DiffViewerDrawer", () => {
  beforeEach(() => {
    resetDiffViewerForTests();
    setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
    syncDisplayPrefsFromSnapshot();
    document.body.innerHTML = "";
    openInExternalEditor.mockClear();
    revealInFileManager.mockClear();
    addToChat.mockClear();
  });

  it("renders an in-hand diff payload with the diff view and New file badge", async () => {
    render(() => (
      <DiffViewerDrawer
        request={diffViewerFixture({
          projectId: "p1",
          path: "src/new-file.ts",
          before: null,
          after: "const v = 2;\n",
        })}
        rootRefs={[]}
      />
    ));
    expect(screen.getByTestId("diff-viewer-path").textContent).toContain(
      "new-file.ts",
    );
    expect(screen.getByTestId("diff-viewer-path").getAttribute("data-file-change")).toBe("added");
    expect(screen.getByText("New file")).toBeTruthy();
    await waitFor(() => expect(document.querySelectorAll(".cm-den-reader .cm-line").length).toBeGreaterThan(0));
    expect(document.querySelector(".cm-den-reader-add"), screen.getByTestId("diff-viewer-body").textContent ?? "").toBeNull();
    expect(screen.getByTestId("diff-viewer-split")).toHaveProperty("disabled", true);
    expect(screen.queryByTestId("files-editor")).toBeNull();
  });

  it("shows a deleted file's last contents as removed lines, without a side to split", async () => {
    render(() => (
      <DiffViewerDrawer
        request={diffViewerFixture({ projectId: "p1", path: "src/gone.ts", before: "a\nb\n", after: null })}
        rootRefs={[]}
      />
    ));
    expect(screen.getByTestId("diff-viewer-path").getAttribute("data-file-change")).toBe("deleted");
    expect(screen.getByText("Deleted file")).toBeTruthy();
    expect(screen.getByTestId("diff-viewer-split")).toHaveProperty("disabled", true);
    await waitFor(() => expect(document.querySelectorAll(".cm-den-reader-delete"), screen.getByTestId("diff-viewer-body").textContent ?? "").toHaveLength(2));
  });

  it("keeps an edit to an existing file tinted as a diff", async () => {
    render(() => (
      <DiffViewerDrawer
        request={diffViewerFixture({ projectId: "p1", path: "src/app.ts", before: "a\n", after: "b\n" })}
        rootRefs={[]}
      />
    ));
    expect(screen.getByTestId("diff-viewer-path").hasAttribute("data-file-change")).toBe(false);
    expect(screen.getByTestId("diff-viewer-split")).toHaveProperty("disabled", false);
    await waitFor(() => expect(document.querySelector(".cm-den-reader-add"), screen.getByTestId("diff-viewer-body").textContent ?? "").toBeTruthy());
  });

  it("offers Reveal and Open in editor in the more menu with a resolved absolutePath", async () => {
    render(() => (
      <DiffViewerDrawer
        request={diffViewerFixture({
          projectId: "p1",
          path: "src/app.ts",
          before: "a",
          after: "b",
          absolutePath: "/repo/src/app.ts",
          rootId: "r1",
        })}
        rootRefs={[{ id: "r1", path: "/repo" }]}
      />
    ));
    fireEvent.click(screen.getByTestId("diff-viewer-more"));
    fireEvent.click(await screen.findByTestId("open-in-menu"));
    fireEvent.click(await screen.findByTestId("open-in-editor"));
    expect(openInExternalEditor).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByTestId("diff-viewer-more"));
    fireEvent.click(await screen.findByTestId("open-in-menu"));
    fireEvent.click(await screen.findByTestId("open-in-file-manager"));
    expect(revealInFileManager).toHaveBeenCalledWith("/repo/src/app.ts", [
      "/repo",
    ]);

    fireEvent.click(screen.getByTestId("diff-viewer-more"));
    fireEvent.click(await screen.findByTestId("diff-viewer-add-to-chat"));
    expect(addToChat).toHaveBeenCalledWith({
      kind: "path-file",
      projectId: "p1",
      rootId: "r1",
      path: "src/app.ts",
      name: "app.ts",
    });
  });

  it("hides the more menu without an absolutePath", () => {
    render(() => (
      <DiffViewerDrawer
        request={diffViewerFixture({ path: "src/app.ts", before: "a", after: "b" })}
        rootRefs={[]}
      />
    ));
    expect(screen.queryByTestId("diff-viewer-more")).toBeNull();
  });

  it("opens copy variants from right-click on Copy path", async () => {
    render(() => (
      <DiffViewerDrawer
        request={diffViewerFixture({
          projectId: "p1",
          path: "src/app.ts",
          before: "a",
          after: "b",
          absolutePath: "/repo/src/app.ts",
        })}
        rootRefs={[]}
      />
    ));
    fireEvent.contextMenu(screen.getByTestId("diff-viewer-copy"));
    expect(await screen.findByTestId("diff-viewer-copy-path")).toBeTruthy();
    expect(screen.getByTestId("diff-viewer-copy-relative-path")).toBeTruthy();
    expect(screen.getByTestId("diff-viewer-copy-file-name")).toBeTruthy();
  });

  it("toggles the diff layout between unified and split", async () => {
    render(() => (
      <DiffViewerDrawer
        request={diffViewerFixture({ projectId: "p1", path: "a.ts", before: "a", after: "b" })}
        rootRefs={[]}
      />
    ));
    const unified = screen.getByTestId("diff-viewer-unified");
    const split = screen.getByTestId("diff-viewer-split");
    expect(unified.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(split);
    await waitFor(() => {
      expect(split.getAttribute("aria-pressed")).toBe("true");
    });
    expect(unified.getAttribute("aria-pressed")).toBe("false");
    await waitFor(() => expect(document.querySelectorAll(".cm-editor")).toHaveLength(2));
    fireEvent.click(unified);
    await waitFor(() => expect(document.querySelectorAll(".cm-editor")).toHaveLength(1));
  });

  it("reveals the diff's root-addressed path and dismisses the overlay", async () => {
    const { registerOpenSourceSink, registerOpenSourceProjectLookup } = await import("../../../platform/navigation/open-source.ts");
    const { openDiffViewer, diffViewerRequest } = await import("../../../platform/navigation/in-app-diff.ts");
    const request = { projectId: "p1", rootId: "r1", path: "src/app.ts", absolutePath: "/repo/src/app.ts", before: "a", after: "b" };
    const sink = vi.fn();
    const stop = registerOpenSourceSink(sink);
    const stopLookup = registerOpenSourceProjectLookup(() => ({ roots: [{ id: "r1", path: "/repo" }] }));
    try {
      openDiffViewer(diffViewerFixture(request));
      render(() => <DiffViewerDrawer request={diffViewerFixture(request)} rootRefs={[{ id: "r1", path: "/repo" }]} />);
      fireEvent.click(screen.getByTestId("diff-viewer-more"));
      fireEvent.click(await screen.findByTestId("diff-viewer-reveal-tree"));
      expect(sink).toHaveBeenCalledWith(expect.objectContaining({ action: "reveal", rootId: "r1", path: "src/app.ts" }));
      await waitFor(() => expect(diffViewerRequest()).toBeNull());
    } finally { stop(); stopLookup(); }
  });
});
