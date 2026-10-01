import { stubClient } from "../../test/client-fixture.ts";
import { cleanup, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyFilesBufferLoad } from "../documents/project-files-buffers.ts";
import { FilesImageViewer } from "./FilesImageViewer.tsx";
import { createSignal } from "solid-js";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import {
  ROOTS,
  loadedBuffer,
  resetProjectFilesViewTest,
} from "../components/project-files-view-test-harness.ts";

describe("FilesImageViewer workspace binding", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it("waits for decoding and reuses the image across resident activation", async () => {
    let finishDecode!: () => void;
    let loaded!: () => void;
    class ImageDecode {
      onload?: () => void;
      naturalWidth = 100;
      naturalHeight = 50;
      set src(_value: string) { loaded = () => this.onload?.(); }
      decode() { return new Promise<void>((resolve) => { finishDecode = resolve; }); }
    }
    vi.stubGlobal("Image", ImageDecode);
    const [active, setActive] = createSignal(true);
    const buffer = loadedBuffer({ path: "logo.png", kind: "image" });
    const fetch = vi.fn(async () => new Blob(["image"]));
    const client = stubClient({ getProjectSourceRaw: fetch });
    render(() => <ResidentPresenceProvider presence={active() ? "active" : "idle"}>
      <FilesImageViewer projectId="p1" buffer={buffer} roots={ROOTS} client={client}
        onInnerLayerChange={vi.fn()} onRevealSegment={vi.fn()} />
    </ResidentPresenceProvider>);
    await waitFor(() => expect(loaded).toBeDefined());
    loaded();
    expect(screen.queryByTestId("files-image-img")).toBeNull();
    finishDecode();
    const image = await screen.findByTestId("files-image-img");
    setActive(false);
    setActive(true);
    await Promise.resolve();
    expect(fetch).toHaveBeenCalledOnce();
    expect(screen.getByTestId("files-image-img")).toBe(image);
    expect(image.getAttribute("width")).toBe("100");
  });

  it("keeps the decoded image painted until refreshed bytes are decoded", async () => {
    const pending: Decode[] = [];
    class Decode {
      onload?: () => void;
      onerror?: () => void;
      naturalWidth = 100;
      naturalHeight = 50;
      set src(_value: string) { pending.push(this); }
      decode() { return Promise.resolve(); }
    }
    let serial = 0;
    vi.stubGlobal("Image", Decode);
    vi.stubGlobal("URL", {
      createObjectURL: () => `blob:image-${++serial}`,
      revokeObjectURL: vi.fn(),
    });
    const buffer = loadedBuffer({ path: "logo.png", kind: "image" });
    render(() => <FilesImageViewer projectId="p1" buffer={buffer} roots={ROOTS}
      client={stubClient({ getProjectSourceRaw: async () => new Blob(["image"]) })}
      onInnerLayerChange={vi.fn()} onRevealSegment={vi.fn()} />);
    await waitFor(() => expect(pending).toHaveLength(1));
    expect(screen.queryByTestId("files-image-img")).toBeNull();
    pending[0]!.onload?.();
    const image = await screen.findByTestId("files-image-img");
    expect(image.getAttribute("src")).toBe("blob:image-1");
    applyFilesBufferLoad("p1", buffer.key, {
      file_id: "", version_id: "", workspace_id: "workspace-1", workspace_kind: "project",
      path: "logo.png", content: "", binary: true, over_limit: false, writable: true,
      size_bytes: 100, mime: "image/png",
    });
    await waitFor(() => expect(pending).toHaveLength(2));
    expect(screen.getByTestId("files-image-img")).toBe(image);
    expect(image.getAttribute("src")).toBe("blob:image-1");
    expect(screen.queryByTestId("files-image-loading")).toBeNull();
    pending[1]!.onload?.();
    await waitFor(() => expect(image.getAttribute("src")).toBe("blob:image-2"));
    expect(screen.getByTestId("files-image-img")).toBe(image);
    expect(image.getAttribute("src")).toBe("blob:image-2");
  });

  it("reloads image bytes when the source revision changes at the same path", async () => {
    const buffer = loadedBuffer({ path: "logo.png", kind: "image" });
    const getProjectSourceRaw = vi.fn(async () => new Blob(["image"], { type: "image/png" }));
    render(() => <FilesImageViewer projectId="p1" buffer={buffer} roots={ROOTS}
      client={stubClient({ getProjectSourceRaw })}
      onInnerLayerChange={vi.fn()} onRevealSegment={vi.fn()} />);
    await waitFor(() => expect(getProjectSourceRaw).toHaveBeenCalledOnce());
    applyFilesBufferLoad("p1", buffer.key, {
      file_id: "", version_id: "", workspace_id: "workspace-1", workspace_kind: "project",
      path: "logo.png", content: "", binary: true, over_limit: false, writable: true,
      size_bytes: 100, mime: "image/png",
    });
    await waitFor(() => expect(getProjectSourceRaw).toHaveBeenCalledTimes(2));
  });

});
