import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { ArtifactListItem } from "../../api/types.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { ArtifactGalleryTile } from "./ArtifactGalleryTile.tsx";

function pngBlob(tag = 0): Blob {
  const bytes = Uint8Array.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49,
    0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
    0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, tag,
  ]);
  return new Blob([bytes], { type: "image/png" });
}

function makeItem(overrides: Partial<ArtifactListItem> = {}): ArtifactListItem {
  return {
    id: "art-1",
    mime: "image/png",
    source: "user",
    content_hash: "hash-a",
    caption: "First upload",
    session_id: "sess-1111-2222-3333-444444444444",
    created_at: "2026-07-01T12:00:00Z",
    ...overrides,
  };
}

describe("ArtifactGalleryTile", () => {
  const getSessionArtifact = vi.fn();
  const onOpen = vi.fn();
  const onSrc = vi.fn();
  const onRequestDelete = vi.fn();
  const mockClient = {
    getSessionArtifact,
  } as unknown as LycaonClient;

  let nextUrlId = 0;
  let createObjectURLSpy: ReturnType<typeof vi.spyOn>;
  let revokeObjectURLSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    vi.clearAllMocks();
    nextUrlId = 0;
    createObjectURLSpy = vi
      .spyOn(URL, "createObjectURL")
      .mockImplementation(() => `blob:test-preview-${++nextUrlId}`);
    revokeObjectURLSpy = vi
      .spyOn(URL, "revokeObjectURL")
      .mockImplementation(() => {});
  });

  afterEach(() => {
    createObjectURLSpy.mockRestore();
    revokeObjectURLSpy.mockRestore();
  });

  it("preserves blob URL, DOM image, and revealed state across metadata and sibling updates", async () => {
    getSessionArtifact.mockImplementation(async () => pngBlob(1));
    const [items, setItems] = createSignal<ArtifactListItem[]>([makeItem()]);

    render(() => (
      <KeyedIndex each={items()} keyOf={(entry) => entry.id}>
        {(item, index) => (
          <ArtifactGalleryTile
            item={item()}
            index={index()}
            total={items().length}
            projectId="proj-1"
            client={mockClient}
            onOpen={onOpen}
            onSrc={onSrc}
            onRequestDelete={onRequestDelete}
          />
        )}
      </KeyedIndex>
    ));

    const thumb = await screen.findByRole("button", { name: "Enlarge First upload" });
    const img = await waitFor(() => {
      const el = thumb.querySelector("img");
      if (!el) throw new Error("img not mounted");
      return el;
    });
    fireEvent.load(img);
    await waitFor(() => {
      expect(thumb.hasAttribute("disabled")).toBe(false);
    });

    const tile = screen.getByTestId("project-artifact-tile");
    expect(tile.classList.contains("den-transcript-visual--ready")).toBe(true);
    expect(img.getAttribute("src")).toBe("blob:test-preview-1");
    expect(getSessionArtifact).toHaveBeenCalledTimes(1);
    expect(revokeObjectURLSpy).not.toHaveBeenCalled();
    expect(onSrc).toHaveBeenCalledWith("art-1", "blob:test-preview-1");

    setItems([
      makeItem({
        id: "art-2",
        content_hash: "hash-sibling",
        caption: "Newest upload",
        created_at: "2026-07-02T12:00:00Z",
      }),
      makeItem({
        caption: "Updated caption",
        references: [{ kind: "message_attachment", count: 1 }],
      }),
    ]);

    const updatedThumb = await screen.findByRole("button", {
      name: "Enlarge Updated caption",
    });
    expect(updatedThumb).toBe(thumb);
    expect(updatedThumb.querySelector("img")).toBe(img);
    expect(img.getAttribute("src")).toBe("blob:test-preview-1");
    expect(updatedThumb.hasAttribute("disabled")).toBe(false);
    expect(tile.classList.contains("den-transcript-visual--ready")).toBe(true);
    expect(
      getSessionArtifact.mock.calls.filter(([, id]) => id === "art-1"),
    ).toHaveLength(1);
    expect(revokeObjectURLSpy).not.toHaveBeenCalled();
  });

  it("holds the previous preview while content_hash replacement loads and revokes it on settle and unmount", async () => {
    let resolveReplacement!: (blob: Blob) => void;
    getSessionArtifact
      .mockResolvedValueOnce(pngBlob(1))
      .mockImplementationOnce(
        () =>
          new Promise<Blob>((resolve) => {
            resolveReplacement = resolve;
          }),
      );

    const [item, setItem] = createSignal<ArtifactListItem>(makeItem());
    const { unmount } = render(() => (
      <ArtifactGalleryTile
        item={item()}
        index={0}
        total={1}
        projectId="proj-1"
        client={mockClient}
        onOpen={onOpen}
        onSrc={onSrc}
        onRequestDelete={onRequestDelete}
      />
    ));

    const thumb = await screen.findByRole("button", { name: "Enlarge First upload" });
    const img = await waitFor(() => {
      const el = thumb.querySelector("img");
      if (!el) throw new Error("img not mounted");
      return el;
    });
    fireEvent.load(img);
    await waitFor(() => {
      expect(thumb.hasAttribute("disabled")).toBe(false);
    });

    setItem(makeItem({ content_hash: "hash-b" }));
    await waitFor(() => {
      expect(getSessionArtifact).toHaveBeenCalledTimes(2);
    });

    expect(img.getAttribute("src")).toBe("blob:test-preview-1");
    expect(thumb.hasAttribute("disabled")).toBe(false);
    expect(revokeObjectURLSpy).not.toHaveBeenCalled();

    resolveReplacement(pngBlob(2));
    await waitFor(() => {
      expect(img.getAttribute("src")).toBe("blob:test-preview-2");
    });
    expect(thumb.querySelector("img")).toBe(img);
    expect(thumb.hasAttribute("disabled")).toBe(false);
    expect(onSrc).toHaveBeenLastCalledWith("art-1", "blob:test-preview-2");
    expect(revokeObjectURLSpy).toHaveBeenCalledTimes(1);
    expect(revokeObjectURLSpy).toHaveBeenCalledWith("blob:test-preview-1");

    unmount();
    expect(revokeObjectURLSpy).toHaveBeenCalledTimes(2);
    expect(revokeObjectURLSpy).toHaveBeenLastCalledWith("blob:test-preview-2");
    expect(onSrc).toHaveBeenLastCalledWith("art-1", null);
  });

  it("releases an in-flight preview when unmounted before fetch settles", async () => {
    let resolveFetch!: (blob: Blob) => void;
    getSessionArtifact.mockImplementationOnce(
      () =>
        new Promise<Blob>((resolve) => {
          resolveFetch = resolve;
        }),
    );

    const { unmount } = render(() => (
      <ArtifactGalleryTile
        item={makeItem()}
        index={0}
        total={1}
        projectId="proj-1"
        client={mockClient}
        onOpen={onOpen}
        onSrc={onSrc}
        onRequestDelete={onRequestDelete}
      />
    ));

    await waitFor(() => {
      expect(getSessionArtifact).toHaveBeenCalledTimes(1);
    });
    unmount();

    resolveFetch(pngBlob(3));
    await waitFor(() => {
      expect(revokeObjectURLSpy).toHaveBeenCalledWith("blob:test-preview-1");
    });
    expect(onSrc).not.toHaveBeenCalledWith("art-1", "blob:test-preview-1");
  });
});
