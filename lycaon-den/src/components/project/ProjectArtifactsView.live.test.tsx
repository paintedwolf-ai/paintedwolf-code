import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { ArtifactListItem } from "../../api/types.ts";
import {
  ARTIFACT_REFRESH_DEBOUNCE_MS,
  clearArtifactDeletionMemory,
  notifyArtifactChanged,
  requestArtifactResync,
} from "../../chat/visual/artifact-change-store.ts";
import {
  clearVisualArtifactSrcs,
  visualArtifactSrc,
  visualArtifactSrcSync,
} from "../../chat/visual/visual-artifact-src.ts";
import { createAppStore } from "../../store/app-state.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { resetSurfaceQueriesForTests } from "../../ui/surface-query.ts";
import { ProjectArtifactsView } from "./ProjectArtifactsView.tsx";

const SESSION_ID = "sess-aaaa-bbbb-cccc-dddddddddddd";

function pngBlob(tag = 0): Blob {
  const bytes = Uint8Array.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49,
    0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
    0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, tag,
  ]);
  return new Blob([bytes], { type: "image/png" });
}

function makeItem(
  id: string,
  caption: string,
  createdAt: string,
  overrides: Partial<ArtifactListItem> = {},
): ArtifactListItem {
  return {
    id,
    mime: "image/png",
    source: "user",
    content_hash: `hash-${id}`,
    caption,
    session_id: SESSION_ID,
    created_at: createdAt,
    ...overrides,
  };
}

async function readyThumb(name: string): Promise<{
  thumb: HTMLElement;
  img: HTMLImageElement;
}> {
  const thumb = await screen.findByRole("button", { name });
  const img = await waitFor(() => {
    const el = thumb.querySelector("img");
    if (!el) throw new Error("img not mounted");
    return el;
  });
  fireEvent.load(img);
  await waitFor(() => {
    expect(thumb.hasAttribute("disabled")).toBe(false);
  });
  return { thumb, img };
}

describe("ProjectArtifactsView live updates", () => {
  const listProjectArtifacts = vi.fn();
  const getSessionArtifact = vi.fn();
  const deleteProjectArtifact = vi.fn();
  const mockClient = {
    listProjectArtifacts,
    getSessionArtifact,
    deleteProjectArtifact,
  } as unknown as LycaonClient;

  let nextUrlId = 0;
  let createObjectURLSpy: ReturnType<typeof vi.spyOn>;
  let revokeObjectURLSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    clearArtifactDeletionMemory();
    clearVisualArtifactSrcs();
    resetSurfaceQueriesForTests();
    nextUrlId = 0;
    createObjectURLSpy = vi
      .spyOn(URL, "createObjectURL")
      .mockImplementation(() => `blob:live-preview-${++nextUrlId}`);
    revokeObjectURLSpy = vi
      .spyOn(URL, "revokeObjectURL")
      .mockImplementation(() => {});
    getSessionArtifact.mockImplementation(async (_sid: string, id: string) =>
      pngBlob(id.charCodeAt(id.length - 1)),
    );
  });

  afterEach(() => {
    vi.useRealTimers();
    clearArtifactDeletionMemory();
    clearVisualArtifactSrcs();
    createObjectURLSpy.mockRestore();
    revokeObjectURLSpy.mockRestore();
    for (const el of document.querySelectorAll('[data-testid="project-artifacts-lightbox"]')) {
      el.remove();
    }
  });

  it("coalesces double live events, prepends newest first, and keeps decoded tiles interactive", async () => {
    const art1 = makeItem("art-1", "First upload", "2026-07-01T12:00:00Z");
    const art2 = makeItem("art-2", "Second upload", "2026-07-02T12:00:00Z");
    const art3 = makeItem("art-3", "Third upload", "2026-07-03T12:00:00Z");
    let current = [art2, art1];
    listProjectArtifacts.mockImplementation(async () => ({
      artifacts: current,
      facets: { session_ids: [SESSION_ID] },
    }));

    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} client={mockClient} />
    ));

    const { thumb: thumb2, img: img2 } = await readyThumb("Enlarge Second upload");
    const { thumb: thumb1, img: img1 } = await readyThumb("Enlarge First upload");
    const src2 = img2.getAttribute("src");
    const src1 = img1.getAttribute("src");
    expect(listProjectArtifacts).toHaveBeenCalledTimes(1);
    expect(getSessionArtifact).toHaveBeenCalledTimes(2);
    expect(revokeObjectURLSpy).not.toHaveBeenCalled();

    current = [
      art3,
      {
        ...art2,
        evidence_handle: "img2",
        references: [{ kind: "message_attachment", count: 1 }],
      },
      art1,
    ];
    // Commit and evidence-handle binding emit back-to-back written events with the same hash.
    notifyArtifactChanged({
      project_id: "proj-1",
      artifact_id: "art-3",
      op: "written",
      content_hash: "hash-art-3",
    });
    const transcriptFetch = visualArtifactSrc(mockClient, SESSION_ID, "art-3");
    notifyArtifactChanged({
      project_id: "proj-1",
      artifact_id: "art-3",
      op: "written",
      content_hash: "hash-art-3",
    });
    const transcriptSrc3 = await transcriptFetch;
    expect(visualArtifactSrcSync(SESSION_ID, "art-3")).toBe(transcriptSrc3);

    const { thumb: thumb3, img: img3 } = await readyThumb("Enlarge Third upload");
    expect(listProjectArtifacts).toHaveBeenCalledTimes(2);
    expect(getSessionArtifact).toHaveBeenCalledTimes(4);
    expect(revokeObjectURLSpy).not.toHaveBeenCalled();

    const tiles = screen.getAllByTestId("project-artifact-tile");
    expect(tiles.map((el) => el.getAttribute("data-artifact-id"))).toEqual([
      "art-3",
      "art-2",
      "art-1",
    ]);
    expect(tiles[1]!.classList.contains("den-transcript-visual--ready")).toBe(true);
    expect(tiles[2]!.classList.contains("den-transcript-visual--ready")).toBe(true);
    expect(thumb2.querySelector("img")).toBe(img2);
    expect(thumb1.querySelector("img")).toBe(img1);
    expect(img2.getAttribute("src")).toBe(src2);
    expect(img1.getAttribute("src")).toBe(src1);
    expect(thumb2.hasAttribute("disabled")).toBe(false);
    expect(thumb1.hasAttribute("disabled")).toBe(false);

    fireEvent.click(thumb2);
    const lightbox2 = await screen.findByTestId("project-artifacts-lightbox");
    expect(lightbox2.querySelector("img")?.getAttribute("src")).toBe(src2);
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(screen.queryByTestId("project-artifacts-lightbox")).toBeNull());

    fireEvent.click(thumb3);
    const lightbox3 = await screen.findByTestId("project-artifacts-lightbox");
    expect(lightbox3.querySelector("img")?.getAttribute("src")).toBe(img3.getAttribute("src"));
  });

  it("replaces bytes in place when content_hash changes and updates open lightbox URL", async () => {
    const art1 = makeItem("art-1", "First upload", "2026-07-01T12:00:00Z");
    const art2 = makeItem("art-2", "Second upload", "2026-07-02T12:00:00Z", {
      content_hash: "hash-2a",
    });
    let current = [art2, art1];
    listProjectArtifacts.mockImplementation(async () => ({ artifacts: current }));

    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} client={mockClient} />
    ));

    const { thumb: thumb2, img: img2 } = await readyThumb("Enlarge Second upload");
    const { img: img1 } = await readyThumb("Enlarge First upload");
    const initialSrc2 = img2.getAttribute("src");
    const initialSrc1 = img1.getAttribute("src");

    fireEvent.click(thumb2);
    const lightbox = await screen.findByTestId("project-artifacts-lightbox");
    expect(lightbox.querySelector("img")?.getAttribute("src")).toBe(initialSrc2);

    let resolveReplacement!: (blob: Blob) => void;
    getSessionArtifact.mockImplementationOnce(
      () =>
        new Promise<Blob>((resolve) => {
          resolveReplacement = resolve;
        }),
    );
    current = [{ ...art2, content_hash: "hash-2b" }, art1];
    notifyArtifactChanged({
      project_id: "proj-1",
      artifact_id: "art-2",
      op: "written",
      content_hash: "hash-2b",
    });

    await waitFor(() => expect(getSessionArtifact).toHaveBeenCalledTimes(3));
    expect(img2.getAttribute("src")).toBe(initialSrc2);
    expect(thumb2.hasAttribute("disabled")).toBe(false);
    expect(revokeObjectURLSpy).not.toHaveBeenCalled();

    resolveReplacement(pngBlob(99));
    await waitFor(() => {
      expect(img2.getAttribute("src")).not.toBe(initialSrc2);
    });
    const replacedSrc2 = img2.getAttribute("src");
    expect(lightbox.querySelector("img")?.getAttribute("src")).toBe(replacedSrc2);
    expect(revokeObjectURLSpy).toHaveBeenCalledTimes(1);
    expect(revokeObjectURLSpy).toHaveBeenCalledWith(initialSrc2);
    expect(img1.getAttribute("src")).toBe(initialSrc1);
    expect(getSessionArtifact.mock.calls.filter(([, id]) => id === "art-1")).toHaveLength(1);
  });

  it("refreshes the active Page 2 cursor in place without resetting to Page 1", async () => {
    const page1 = [
      makeItem("art-2", "Second upload", "2026-07-02T12:00:00Z"),
      makeItem("art-1", "First upload", "2026-07-01T12:00:00Z"),
    ];
    const page2 = [makeItem("art-0", "Older upload", "2026-06-30T12:00:00Z")];
    listProjectArtifacts.mockImplementation(
      async (_pid: string, query?: { cursor?: string }) =>
        query?.cursor === "cursor-1"
          ? { artifacts: page2 }
          : { artifacts: page1, next_cursor: "cursor-1" },
    );

    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} client={mockClient} />
    ));

    await readyThumb("Enlarge Second upload");
    fireEvent.click(screen.getByTestId("project-artifacts-pager-next"));
    const { thumb: thumb0, img: img0 } = await readyThumb("Enlarge Older upload");
    expect(screen.getByText("Page 2")).toBeTruthy();

    const callsBefore = listProjectArtifacts.mock.calls.length;
    const revokesBefore = revokeObjectURLSpy.mock.calls.length;
    const src0 = img0.getAttribute("src");

    notifyArtifactChanged({ project_id: "proj-1", artifact_id: "art-3", op: "written" });
    await waitFor(() => {
      expect(listProjectArtifacts).toHaveBeenCalledTimes(callsBefore + 1);
    });
    expect(listProjectArtifacts).toHaveBeenLastCalledWith("proj-1", {
      limit: 12,
      cursor: "cursor-1",
    });
    expect(screen.getByText("Page 2")).toBeTruthy();
    expect(thumb0.querySelector("img")).toBe(img0);
    expect(img0.getAttribute("src")).toBe(src0);
    expect(thumb0.hasAttribute("disabled")).toBe(false);
    expect(revokeObjectURLSpy).toHaveBeenCalledTimes(revokesBefore);
  });

  it("coalesces a 5-event burst within the debounce window into one list refresh", async () => {
    listProjectArtifacts.mockResolvedValue({
      artifacts: [makeItem("art-1", "First upload", "2026-07-01T12:00:00Z")],
    });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} client={mockClient} />
    ));
    await readyThumb("Enlarge First upload");
    expect(listProjectArtifacts).toHaveBeenCalledTimes(1);

    vi.useFakeTimers();
    for (let i = 0; i < 5; i++) {
      notifyArtifactChanged({ project_id: "proj-1", artifact_id: `burst-${i}`, op: "written" });
      if (i < 4) await vi.advanceTimersByTimeAsync(4);
    }
    await vi.advanceTimersByTimeAsync(ARTIFACT_REFRESH_DEBOUNCE_MS - 1);
    expect(listProjectArtifacts).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(listProjectArtifacts).toHaveBeenCalledTimes(2);
  });

  it("defers while resident-idle, revalidates cleanly on activation, and resyncs on reconnect", async () => {
    const art1 = makeItem("art-1", "First upload", "2026-07-01T12:00:00Z");
    const art2 = makeItem("art-2", "Idle upload", "2026-07-02T12:00:00Z");
    const art3 = makeItem("art-3", "Gap upload", "2026-07-03T12:00:00Z");
    let current = [art1];
    listProjectArtifacts.mockImplementation(async () => ({ artifacts: current }));

    const [presence, setPresence] = createSignal<"active" | "idle" | "pending">("active");
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <ProjectArtifactsView projectId="proj-1" appStore={appStore} client={mockClient} />
      </ResidentPresenceProvider>
    ));

    const { thumb: thumb1, img: img1 } = await readyThumb("Enlarge First upload");
    const callsAfterMount = listProjectArtifacts.mock.calls.length;

    setPresence("idle");
    current = [art2, art1];
    vi.useFakeTimers();
    notifyArtifactChanged({
      project_id: "proj-1",
      artifact_id: "art-2",
      op: "written",
      content_hash: "hash-art-2",
    });
    await vi.advanceTimersByTimeAsync(ARTIFACT_REFRESH_DEBOUNCE_MS + 30);
    vi.useRealTimers();
    expect(listProjectArtifacts).toHaveBeenCalledTimes(callsAfterMount);

    let resolveWake!: (value: { artifacts: ArtifactListItem[] }) => void;
    listProjectArtifacts.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveWake = resolve;
        }),
    );
    setPresence("pending");
    await waitFor(() => {
      expect(listProjectArtifacts).toHaveBeenCalledTimes(callsAfterMount + 1);
    });
    expect(screen.queryByTestId("project-artifacts-loading")).toBeNull();
    expect(thumb1.hasAttribute("disabled")).toBe(false);
    resolveWake({ artifacts: [art2, art1] });

    await readyThumb("Enlarge Idle upload");
    setPresence("active");
    current = [art3, art2, art1];
    requestArtifactResync("proj-1");

    await readyThumb("Enlarge Gap upload");
    expect(
      screen.getAllByTestId("project-artifact-tile").map((el) => el.getAttribute("data-artifact-id")),
    ).toEqual(["art-3", "art-2", "art-1"]);
    expect(thumb1.querySelector("img")).toBe(img1);
    expect(revokeObjectURLSpy).not.toHaveBeenCalled();
  });
});
