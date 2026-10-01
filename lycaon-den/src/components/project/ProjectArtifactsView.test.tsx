import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ArtifactListItem } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createSignal } from "solid-js";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { notifyArtifactChanged } from "../../chat/visual/artifact-change-store.ts";
import { ProjectArtifactsView } from "./ProjectArtifactsView.tsx";
import { FILMSTRIP_MIME } from "../../chat/visual/filmstrip-zip.ts";
import { buildTestFilmstripZip, zipAsBlob } from "../../chat/visual/filmstrip-zip.fixture.ts";
import {
  filterProjectArtifacts,
  uniqueArtifactChatIds,
} from "./project-artifacts-filter.ts";

const listProjectArtifacts = vi.fn();
const getSessionArtifact = vi.fn();
const addToChat = vi.hoisted(() => vi.fn());

const deleteProjectArtifact = vi.fn();

const mockClient = {
  listProjectArtifacts,
  getSessionArtifact,
  deleteProjectArtifact,
};

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/connection/app-connection.ts")>();
  return {
    ...actual,
    getLycaonClient: () => mockClient,
  };
});

vi.mock("../../chat/composer/add-to-chat.ts", () => ({
  addToChat: (...args: unknown[]) => addToChat(...args),
}));

function pngBlob(): Blob {
  const bytes = Uint8Array.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49,
    0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
    0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44,
    0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x00, 0x03, 0x00,
    0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
    0x44, 0xae, 0x42, 0x60, 0x82,
  ]);
  return new Blob([bytes], { type: "image/png" });
}

function fixtureItems(): ArtifactListItem[] {
  return [
    {
      id: "art-render",
      mime: "image/png",
      source: "render",
      caption: "Mockup A",
      session_id: "sess-aaaa-bbbb-cccc-dddddddddddd",
      workflow_run_id: "run-1111-2222-3333-444444444444",
      created_at: "2026-07-01T12:00:00Z",
      // Tool results anchor by call ID, not message ID.
      tool_call_id: "call-render-1",
      origin_message_id: "msg-origin-1",
    },
    {
      id: "art-capture",
      mime: "image/png",
      source: "capture",
      caption: "Screenshot B",
      session_id: "sess-eeee-ffff-0000-111111111111",
      created_at: "2026-07-02T12:00:00Z",
    },
    {
      id: "art-user",
      mime: "image/png",
      source: "user",
      caption: "Upload C",
      session_id: "sess-aaaa-bbbb-cccc-dddddddddddd",
      workflow_run_id: "run-1111-2222-3333-444444444444",
      created_at: "2026-07-03T12:00:00Z",
      origin_message_id: "msg-origin-3",
    },
  ];
}

async function readyThumb(name: string): Promise<HTMLElement> {
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
  return thumb;
}

describe("project-artifacts-filter", () => {
  it("filters by source and chat", () => {
    const items = fixtureItems();
    expect(filterProjectArtifacts(items, "capture", "all")).toHaveLength(1);
    expect(
      filterProjectArtifacts(
        items,
        "all",
        "sess-eeee-ffff-0000-111111111111",
      ),
    ).toHaveLength(1);
    expect(
      filterProjectArtifacts(
        items,
        "all",
        "sess-aaaa-bbbb-cccc-dddddddddddd",
      ),
    ).toHaveLength(2);
    expect(uniqueArtifactChatIds(items)).toEqual([
      "sess-aaaa-bbbb-cccc-dddddddddddd",
      "sess-eeee-ffff-0000-111111111111",
    ]);
  });
});

describe("ProjectArtifactsView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    addToChat.mockReset();
    listProjectArtifacts.mockResolvedValue({ artifacts: fixtureItems() });
    getSessionArtifact.mockImplementation(async () => pngBlob());
  });

  afterEach(() => {
    for (const el of document.querySelectorAll(
      '[data-testid="project-artifacts-lightbox"]',
    )) {
      el.remove();
    }
  });

  it("opens a captured filmstrip and exposes every frame in the gallery", async () => {
    listProjectArtifacts.mockResolvedValue({ artifacts: [{ ...fixtureItems()[1], mime: FILMSTRIP_MIME, caption: "Orchard interaction" }] });
    getSessionArtifact.mockImplementation(async () => zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME));
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <ProjectArtifactsView projectId="proj-1" appStore={appStore} />);
    fireEvent.click(await readyThumb("Enlarge Orchard interaction"));
    const second = await screen.findByRole("option", { name: "click #load" });
    fireEvent.click(second);
    expect(second.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("img", { name: "click #load" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Enlarge click #load" })).toBeNull();
    fireEvent.keyDown(second, { key: "ArrowLeft" });
    expect(screen.getByRole("option", { name: "initial" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByTestId("project-artifacts-lightbox")).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByTestId("project-artifacts-lightbox")).toBeNull();
  });

  it.each(["fetch", "archive", "decode"])("shows %s failure without an endless loading thumbnail", async (failure) => {
    listProjectArtifacts.mockResolvedValue({ artifacts: [{ ...fixtureItems()[1], mime: failure === "archive" ? FILMSTRIP_MIME : "image/png" }] });
    getSessionArtifact.mockImplementation(async () => {
      if (failure === "fetch") throw new Error("unavailable artifact");
      if (failure === "archive") return new Blob(["invalid archive"], { type: FILMSTRIP_MIME });
      return pngBlob();
    });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <ProjectArtifactsView projectId="proj-1" appStore={appStore} />);
    const thumb = await screen.findByRole("button", { name: "Enlarge Screenshot B" });
    if (failure === "decode") {
      const img = await screen.findByRole("img", { name: "Screenshot B" });
      fireEvent.error(img);
    }
    await screen.findByText("This artifact could not be loaded.");
    expect(thumb.getAttribute("aria-busy")).toBe("false");
    expect(thumb.hasAttribute("disabled")).toBe(true);
  });

  it("renders tiles from a mocked list with source badges", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} />
    ));

    expect(await screen.findByTestId("project-artifacts-gallery")).toBeTruthy();
    await waitFor(() => {
      expect(listProjectArtifacts).toHaveBeenCalledWith("proj-1", {
        limit: 12,
      });
    });
    const tiles = screen.getAllByTestId("project-artifact-tile");
    expect(tiles).toHaveLength(3);
    const badges = screen.getAllByTestId("project-artifact-source-badge");
    expect(badges.map((el) => el.textContent)).toEqual([
      "Render",
      "Capture",
      "User",
    ]);
  });

  it("pages through the opaque cursor and keeps prior pages available", async () => {
    const items = fixtureItems();
    listProjectArtifacts
      .mockResolvedValueOnce({ artifacts: items.slice(0, 2), next_cursor: "cursor-1" })
      .mockResolvedValueOnce({ artifacts: [items[2]] });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} />
    ));

    expect(await screen.findAllByTestId("project-artifact-tile")).toHaveLength(2);
    const gallery = screen.getByTestId("project-artifacts-gallery");
    expect(gallery.closest(".den-browse-main--content-scroll")).toBeTruthy();
    expect(gallery.getAttribute("data-den-scrollport")).toBe("y");
    expect(gallery.querySelector(":scope > .den-scrollport__viewport > .den-artifacts-grid__content")).toBeTruthy();
    const pager = screen.getByTestId("project-artifacts-pager");
    expect(gallery.contains(pager)).toBe(false);

    fireEvent.click(screen.getByTestId("project-artifacts-pager-next"));
    await waitFor(() => {
      expect(listProjectArtifacts).toHaveBeenLastCalledWith("proj-1", {
        limit: 12,
        cursor: "cursor-1",
      });
      expect(screen.getAllByTestId("project-artifact-tile")).toHaveLength(1);
    });
    expect(screen.getByText("Page 2")).toBeTruthy();
    expect(
      screen.getByTestId("project-artifacts-pager-next").hasAttribute("disabled"),
    ).toBe(true);

    fireEvent.click(screen.getByTestId("project-artifacts-pager-prev"));
    expect(screen.getAllByTestId("project-artifact-tile")).toHaveLength(2);
    expect(screen.getByText("Page 1")).toBeTruthy();
  });

  it("plays video artifacts and does not offer them as image prompt attachments", async () => {
    listProjectArtifacts.mockResolvedValue({
      artifacts: [
        {
          id: "recording-1",
          mime: "video/mp4",
          source: "capture",
          caption: "Live tool recording",
          session_id: "sess-aaaa-bbbb-cccc-dddddddddddd",
          created_at: "2026-07-03T12:00:00Z",
        },
      ],
    });
    getSessionArtifact.mockResolvedValue(
      new Blob(["movie"], { type: "video/mp4" }),
    );
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <ProjectArtifactsView projectId="proj-1" appStore={appStore} />);

    const tile = await screen.findByTestId("project-artifact-tile");
    const video = await waitFor(() => {
      const element = tile.querySelector("video");
      if (!element) throw new Error("video not mounted");
      return element;
    });
    fireEvent.loadedData(video);
    fireEvent.contextMenu(tile);
    expect(
      screen.getByTestId("menu-add-to-chat").getAttribute("aria-disabled"),
    ).toBe("true");
    expect(screen.getByTestId("menu-delete-artifact")).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => {
      if (screen.queryByTestId("menu-delete-artifact")) {
        throw new Error("context menu still open");
      }
    });

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Enlarge Live tool recording",
      }),
    );
    expect(await screen.findByTestId("project-artifacts-lightbox")).toBeTruthy();
    expect(
      document.querySelector("video.den-transcript-visual-lightbox__img"),
    ).toBeTruthy();
  });

  it("shows empty state when the project has no artifacts", async () => {
    listProjectArtifacts.mockResolvedValue({ artifacts: [] });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-empty" appStore={appStore} />
    ));
    expect(await screen.findByTestId("project-artifacts-empty")).toBeTruthy();
  });

  it("paints a fast cold result without flashing loading or empty chrome", async () => {
    let resolveList!: (value: { artifacts: ArtifactListItem[] }) => void;
    listProjectArtifacts.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveList = resolve;
      }),
    );
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-empty" appStore={appStore} />
    ));

    expect(screen.queryByTestId("project-artifacts-loading")).toBeNull();
    expect(screen.queryByTestId("project-artifacts-empty")).toBeNull();
    resolveList({ artifacts: [] });

    expect(await screen.findByTestId("project-artifacts-empty")).toBeTruthy();
    expect(screen.queryByTestId("project-artifacts-loading")).toBeNull();
  });

  it("defers artifact refresh while its resident surface is idle", async () => {
    const [presence, setPresence] = createSignal<"active" | "idle" | "pending">(
      "active",
    );
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <ProjectArtifactsView projectId="proj-1" appStore={appStore} />
      </ResidentPresenceProvider>
    ));
    await screen.findByTestId("project-artifacts-gallery");
    const calls = listProjectArtifacts.mock.calls.length;
    setPresence("idle");
    notifyArtifactChanged({ project_id: "proj-1", artifact_id: "new-artifact", op: "written" });
    await Promise.resolve();
    expect(listProjectArtifacts).toHaveBeenCalledTimes(calls);
    let resolveRefresh!: (value: { artifacts: ArtifactListItem[] }) => void;
    listProjectArtifacts.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveRefresh = resolve;
      }),
    );
    setPresence("pending");
    await waitFor(() => {
      expect(listProjectArtifacts.mock.calls.length).toBeGreaterThan(calls);
    });
    expect(screen.getByTestId("project-artifacts-gallery")).toBeTruthy();
    expect(screen.queryByTestId("project-artifacts-loading")).toBeNull();
    resolveRefresh({ artifacts: fixtureItems() });
  });

  it("narrows the grid with source and chat filters", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} />
    ));
    await screen.findByTestId("project-artifacts-gallery");

    fireEvent.click(screen.getByTestId("project-artifacts-source-capture"));
    await waitFor(() => {
      expect(screen.getAllByTestId("project-artifact-tile")).toHaveLength(1);
    });
    expect(
      screen.getByTestId("project-artifact-tile").getAttribute("data-artifact-id"),
    ).toBe("art-capture");

    fireEvent.click(screen.getByTestId("project-artifacts-source-all"));
    fireEvent.click(screen.getByTestId("project-artifacts-chat-filter"));
    const chat = screen
      .getAllByRole("option")
      .find(
        (option) =>
          option.getAttribute("data-value") ===
          "sess-eeee-ffff-0000-111111111111",
      );
    if (!chat) throw new Error("Missing chat filter option");
    fireEvent.click(chat);
    await waitFor(() => {
      expect(screen.getAllByTestId("project-artifact-tile")).toHaveLength(1);
    });
    expect(
      screen.getByTestId("project-artifact-tile").getAttribute("data-artifact-id"),
    ).toBe("art-capture");
  });

  it("opens lightbox on tile click and reveals in transcript", async () => {
    const onReveal = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");

    render(() => (
      <ProjectArtifactsView
        projectId="proj-1"
        appStore={appStore}
        onRevealInTranscript={onReveal}
      />
    ));

    await screen.findByTestId("project-artifacts-gallery");
    const first = await readyThumb("Enlarge Mockup A");
    fireEvent.click(first);

    expect(await screen.findByTestId("project-artifacts-lightbox")).toBeTruthy();
    expect(screen.getByTestId("project-artifacts-lightbox-source").textContent).toBe(
      "Render",
    );

    fireEvent.click(screen.getByTestId("project-artifact-reveal"));
    expect(onReveal).toHaveBeenCalledWith({
      sessionId: "sess-aaaa-bbbb-cccc-dddddddddddd",
      anchor: { chicklet: "tool", anchorId: "call-render-1" },
    });
    await waitFor(() => {
      expect(screen.queryByTestId("project-artifacts-lightbox")).toBeNull();
    });
  });

  it("reveals a user upload at its message row", async () => {
    const onReveal = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");

    render(() => (
      <ProjectArtifactsView
        projectId="proj-1"
        appStore={appStore}
        onRevealInTranscript={onReveal}
      />
    ));

    await screen.findByTestId("project-artifacts-gallery");
    fireEvent.click(await readyThumb("Enlarge Upload C"));
    await screen.findByTestId("project-artifacts-lightbox");

    fireEvent.click(screen.getByTestId("project-artifact-reveal"));
    expect(onReveal).toHaveBeenCalledWith({
      sessionId: "sess-aaaa-bbbb-cccc-dddddddddddd",
      anchor: { chicklet: "message", anchorId: "msg-origin-3" },
    });
  });

  it("hides reveal for an artifact with no addressable row", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");

    render(() => (
      <ProjectArtifactsView
        projectId="proj-1"
        appStore={appStore}
        onRevealInTranscript={vi.fn()}
      />
    ));

    await screen.findByTestId("project-artifacts-gallery");
    fireEvent.click(await readyThumb("Enlarge Screenshot B"));
    await screen.findByTestId("project-artifacts-lightbox");

    expect(screen.queryByTestId("project-artifact-reveal")).toBeNull();
  });

  it("context menu Add to chat emits an artifact ref", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} />
    ));
    await screen.findByTestId("project-artifacts-gallery");
    await readyThumb("Enlarge Mockup A");
    const tile = screen.getAllByTestId("project-artifact-tile")[0]!;
    expect(tile.getAttribute("draggable")).toBe("true");
    fireEvent.contextMenu(tile);
    fireEvent.click(screen.getByTestId("menu-add-to-chat"));
    expect(addToChat).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "artifact",
        projectId: "proj-1",
        artifactId: "art-render",
        name: "Mockup A",
      }),
    );
  });

  it("delete names what refers to the artifact, then tombstones it", async () => {
    listProjectArtifacts.mockResolvedValue({
      artifacts: [
        {
          id: "art-render",
          mime: "image/png",
          source: "render",
          caption: "Mockup A",
          session_id: "sess-aaaa-bbbb-cccc-dddddddddddd",
          created_at: "2026-07-01T12:00:00Z",
          references: [
            { kind: "project_cover", count: 1 },
            { kind: "message_present", count: 2 },
          ],
        } satisfies ArtifactListItem,
      ],
    });
    deleteProjectArtifact.mockResolvedValue(undefined);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectArtifactsView projectId="proj-1" appStore={appStore} />
    ));
    await screen.findByTestId("project-artifacts-gallery");
    await readyThumb("Enlarge Mockup A");

    const tile = screen.getAllByTestId("project-artifact-tile")[0]!;
    fireEvent.contextMenu(tile);
    fireEvent.click(screen.getByTestId("menu-delete-artifact"));

    const impact = await screen.findByTestId("project-artifact-delete-impact");
    // Counts use persisted reference rows.
    expect(impact.textContent).toContain("the project cover");
    expect(impact.textContent).toContain("2 messages that show it");
    expect(impact.textContent).toContain("will read as deleted");

    listProjectArtifacts.mockResolvedValue({ artifacts: [] });
    fireEvent.click(
      screen.getByTestId("project-artifact-delete-confirm-action"),
    );
    await waitFor(() => {
      expect(deleteProjectArtifact).toHaveBeenCalledWith("proj-1", "art-render");
    });
    expect(await screen.findByTestId("project-artifacts-empty")).toBeTruthy();
  });
});
