import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { ErrorBoundary, Show, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetVisualArtifactSessionMemoryForTests } from "../../chat/visual/visual-artifact-reveal.ts";
import { TranscriptVisualArtifact } from "./TranscriptVisualArtifact.tsx";
import { ChatDestinationScope } from "../../chat/composer/chat-destination-scope.tsx";

const addToChat = vi.hoisted(() => vi.fn());

vi.mock("../../chat/composer/add-to-chat.ts", () => ({
  addToChat: (...args: unknown[]) => addToChat(...args),
}));

function pngBlob(): Blob {
  // Minimal 1×1 PNG
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

async function readyThumb(name: string): Promise<HTMLElement> {
  const thumb = await screen.findByRole("button", { name });
  const img = await waitFor(() => {
    const el = thumb.querySelector("img");
    if (!el) throw new Error("img not mounted");
    return el;
  });
  // Fire load because test images are not decoded.
  fireEvent.load(img);
  await waitFor(() => {
    expect(thumb.hasAttribute("disabled")).toBe(false);
  });
  return thumb;
}

describe("TranscriptVisualArtifact", () => {
  afterEach(() => {
    resetVisualArtifactSessionMemoryForTests();
    addToChat.mockReset();
  });

  it("opens and closes a lightbox on click", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });

    render(() => (
      <TranscriptVisualArtifact
        artifact={{
          id: "art-1",
          mime: "image/png",
          source: "render",
          caption: "Login mockup",
        }}
        sessionId="sess-1"
        projectId="proj-1"
        client={client}
        entryKey="visual:art-1"
      />
    ));

    await waitFor(() => {
      expect(client.getSessionArtifact).toHaveBeenCalledWith(
        "sess-1",
        "art-1",
      );
    });

    const thumb = await readyThumb("Enlarge Login mockup");
    fireEvent.click(thumb);
    expect(screen.getByTestId("transcript-visual-lightbox")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Close enlarged visual" }));
    expect(screen.queryByTestId("transcript-visual-lightbox")).toBeNull();
  });

  it("uses a video player for a video artifact", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(
        async () => new Blob(["movie"], { type: "video/mp4" }),
      ),
    });

    render(() => (
      <TranscriptVisualArtifact
        artifact={{
          id: "recording-1",
          mime: "video/mp4",
          source: "capture",
          caption: "Live tool recording",
        }}
        sessionId="sess-1"
        client={client}
      />
    ));

    expect(await screen.findByTestId("live-tool-recording-player")).toBeTruthy();
    await waitFor(() => {
      expect(
        screen.getByTestId("live-tool-recording-player").querySelector("video"),
      ).toBeTruthy();
    });
  });

  it("context menu Add to chat emits an artifact ref to the scoped chat", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });

    render(() => (
      <ChatDestinationScope destination={{ projectId: "proj-1", sessionId: "chat-1" }}>
        <TranscriptVisualArtifact
          artifact={{
            id: "art-1",
            mime: "image/png",
            source: "render",
            caption: "Login mockup",
          }}
          sessionId="sess-1"
          projectId="proj-1"
          client={client}
          entryKey="visual:art-1"
        />
      </ChatDestinationScope>
    ));
    await readyThumb("Enlarge Login mockup");
    const artifact = screen.getByTestId("transcript-visual-artifact");
    expect(artifact.getAttribute("draggable")).toBe("true");
    fireEvent.contextMenu(artifact);
    fireEvent.click(screen.getByTestId("menu-add-to-chat"));
    expect(addToChat).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "artifact",
        projectId: "proj-1",
        artifactId: "art-1",
        name: "Login mockup",
      }),
      { destination: { projectId: "proj-1", sessionId: "chat-1" } },
    );
  });

  it("does not refetch when the artifact object is rebuilt with the same id", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const [artifact, setArtifact] = createSignal({
      id: "art-stable",
      mime: "image/png" as const,
      source: "render" as const,
      caption: "Mock",
    });

    render(() => (
      <TranscriptVisualArtifact
        artifact={artifact()}
        sessionId="sess-1"
        client={client}
        entryKey="visual:art-stable"
      />
    ));

    await readyThumb("Enlarge Mock");
    expect(client.getSessionArtifact).toHaveBeenCalledTimes(1);

    setArtifact({
      id: "art-stable",
      mime: "image/png",
      source: "render",
      caption: "Mock",
    });

    await waitFor(() => {
      expect(screen.getByTestId("transcript-visual-artifact")).toBeTruthy();
    });
    expect(client.getSessionArtifact).toHaveBeenCalledTimes(1);
    expect(
      screen.getByTestId("transcript-visual-artifact").classList.contains(
        "den-transcript-visual--ready",
      ),
    ).toBe(true);
  });

  it("skips fade-up intro when remounting an already-shown artifact id", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const [mounted, setMounted] = createSignal(true);

    const view = render(() => (
      <Show when={mounted()}>
        <TranscriptVisualArtifact
          artifact={{
            id: "art-remount",
            mime: "image/png",
            source: "render",
            caption: "Stable shot",
          }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-remount"
        />
      </Show>
    ));

    await readyThumb("Enlarge Stable shot");
    expect(
      screen
        .getByTestId("transcript-visual-artifact")
        .classList.contains("den-transcript-visual--instant"),
    ).toBe(false);

    setMounted(false);
    await waitFor(() => {
      expect(screen.queryByTestId("transcript-visual-artifact")).toBeNull();
    });
    setMounted(true);

    await waitFor(() => {
      const el = screen.getByTestId("transcript-visual-artifact");
      expect(el.classList.contains("den-transcript-visual--instant")).toBe(true);
      expect(el.classList.contains("den-transcript-visual--ready")).toBe(true);
    });
    view.unmount();
  });

  it("remounts with the cached image in the first frame and no refetch", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const [mounted, setMounted] = createSignal(true);

    const view = render(() => (
      <Show when={mounted()}>
        <TranscriptVisualArtifact
          artifact={{
            id: "art-sync",
            mime: "image/png",
            source: "render",
            caption: "Sync shot",
          }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-sync"
        />
      </Show>
    ));

    await readyThumb("Enlarge Sync shot");
    expect(client.getSessionArtifact).toHaveBeenCalledTimes(1);

    setMounted(false);
    await waitFor(() => {
      expect(screen.queryByTestId("transcript-visual-artifact")).toBeNull();
    });
    setMounted(true);

    // Synchronous image mounting preserves row height during remounts.
    const thumb = screen.getByRole("button", { name: "Enlarge Sync shot" });
    expect(thumb.querySelector("img")).toBeTruthy();
    expect(client.getSessionArtifact).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("reserves the wire aspect ratio before the image loads", () => {
    // The fetch never settles, so no img mounts and no load event fires.
    const client = stubClient({
      getSessionArtifact: vi.fn(() => new Promise<Blob>(() => {})),
    });

    const view = render(() => (
      <TranscriptVisualArtifact
        artifact={{
          id: "art-sized",
          mime: "image/png",
          source: "render",
          caption: "Sized shot",
          width: 1280,
          height: 800,
        }}
        sessionId="sess-1"
        client={client}
        entryKey="visual:art-sized"
      />
    ));

    const thumb = screen.getByRole("button", { name: "Enlarge Sized shot" });
    expect(thumb.querySelector("img")).toBeNull();
    expect(thumb.style.aspectRatio).toBe("1280 / 800");
    expect(thumb.classList.contains("den-transcript-visual__frame--sized")).toBe(true);
    view.unmount();
  });

  it("keeps the placeholder box when the wire carries no dimensions", () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(() => new Promise<Blob>(() => {})),
    });

    const view = render(() => (
      <TranscriptVisualArtifact
        artifact={{
          id: "art-unsized",
          mime: "image/png",
          source: "render",
          caption: "Unsized shot",
        }}
        sessionId="sess-1"
        client={client}
        entryKey="visual:art-unsized"
      />
    ));

    const thumb = screen.getByRole("button", { name: "Enlarge Unsized shot" });
    expect(thumb.style.aspectRatio).toBe("");
    expect(thumb.classList.contains("den-transcript-visual__frame--sized")).toBe(false);
    view.unmount();
  });

  it("closes the lightbox on Escape", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });

    render(() => (
      <TranscriptVisualArtifact
        artifact={{
          id: "art-2",
          mime: "image/png",
          source: "capture",
          caption: "Board",
        }}
        sessionId="sess-1"
        client={client}
        entryKey="visual:art-2"
      />
    ));

    const thumb = await readyThumb("Enlarge Board");
    fireEvent.click(thumb);
    expect(screen.getByTestId("transcript-visual-lightbox")).toBeTruthy();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByTestId("transcript-visual-lightbox")).toBeNull();
  });
});

describe("TranscriptVisualArtifact refused fetch", () => {
  it("states the failure in the card instead of reaching the stage boundary", async () => {
    const stageFailed = vi.fn();
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => {
        throw new Error("artifact 404");
      }),
    });

    render(() => (
      <ErrorBoundary
        fallback={(err) => {
          stageFailed(err);
          return <p data-testid="stage-boundary">Reload view</p>;
        }}
      >
        <TranscriptVisualArtifact
          artifact={{ id: "art-gone", mime: "image/png", source: "capture" }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-gone"
        />
      </ErrorBoundary>
    ));

    await screen.findByTestId("transcript-visual-error");
    expect(stageFailed).not.toHaveBeenCalled();
    expect(screen.queryByTestId("stage-boundary")).toBeNull();
    const frame = screen.getByRole("button", { name: "Enlarge visual artifact" });
    expect(frame.getAttribute("aria-busy")).toBe("false");
  });
});
