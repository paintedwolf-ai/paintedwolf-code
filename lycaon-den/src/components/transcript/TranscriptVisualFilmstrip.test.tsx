import { createSignal } from "solid-js";
import { TranscriptVisualFilmstrip } from "./TranscriptVisualFilmstrip.tsx";
import { stubClient } from "../../test/client-fixture.ts";
import { ErrorBoundary } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { at } from "../../test/at.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { FILMSTRIP_MIME } from "../../chat/visual/filmstrip-zip.ts";
import { buildTestFilmstripZip, zipAsBlob } from "../../chat/visual/filmstrip-zip.fixture.ts";
import { filmstripCache } from "../../chat/visual/frame-archive-cache.ts";
import { TranscriptVisualArtifact } from "./TranscriptVisualArtifact.tsx";

describe("TranscriptVisualArtifact filmstrip", () => {
  afterEach(() => { filmstripCache.clear(); vi.restoreAllMocks(); });

  it.each(["before", "after"])("retains frame URLs across unmount %s the fetch settles until session teardown", async (when) => {
    let release: ((blob: Blob) => void) | undefined;
    const getSessionArtifact = vi.fn(() => new Promise<Blob>((resolve) => { release = resolve; }));
    let created = 0;
    const create = vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:frame-${++created}`);
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const { unmount } = render(() => <TranscriptVisualArtifact
      artifact={{ id: "art-delayed", mime: FILMSTRIP_MIME, source: "capture" }}
      sessionId="sess-1" client={stubClient({ getSessionArtifact })} />);
    expect(release).toBeDefined();
    if (when === "before") unmount();
    release?.(zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME));
    await waitFor(() => expect(create).toHaveBeenCalledOnce());
    if (when === "after") unmount();
    expect(revoke).not.toHaveBeenCalled();
    filmstripCache.clear();
    await waitFor(() => expect(revoke).toHaveBeenCalledOnce());
    expect(revoke.mock.calls.map(([url]) => url)).toEqual(["blob:frame-1"]);
  });

  it("hides previous frames during an artifact change and reuses them on return", async () => {
    const [id, setId] = createSignal("first");
    let finishSecond!: (blob: Blob) => void;
    const getSessionArtifact = vi.fn(async (_session: string, artifact: string) =>
      artifact === "second" ? new Promise<Blob>((resolve) => { finishSecond = resolve; })
        : zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME));
    const client = stubClient({ getSessionArtifact });
    const mounted = render(() => <TranscriptVisualFilmstrip
      artifact={{ id: id(), mime: FILMSTRIP_MIME, source: "capture" }} sessionId="s" client={client} />);
    await screen.findByTestId("transcript-visual-filmstrip-rail");
    const first = mounted.container.querySelector("img")?.getAttribute("src");
    expect(first).toMatch(/^blob:/);
    setId("second");
    expect(mounted.container.querySelector("img")).toBeNull();
    finishSecond(zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME));
    await screen.findByTestId("transcript-visual-filmstrip-rail");
    expect(mounted.container.querySelector("img")?.getAttribute("src")).not.toBe(first);
    setId("first");
    expect(mounted.container.querySelector("img")?.getAttribute("src")).toBe(first);
    expect(getSessionArtifact).toHaveBeenCalledTimes(2);
    mounted.unmount();
  });

  it("scrubs frames with a labeled step rail and reading pane", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => {
        return zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME);
      }),
    });

    const toolOutput = `[page#2]
${JSON.stringify({
  final_url: "http://127.0.0.1:9/",
  frames: [
    { index: 0, caption: "initial", evidence_handle: "page#1" },
    { index: 1, caption: "click #load", evidence_handle: "page#2" },
  ],
  action_results: [{ ok: true, type: "click", selector: "#load" }],
})}`;

    render(() => (
      <TranscriptVisualArtifact
        artifact={{
          id: "art-strip",
          mime: FILMSTRIP_MIME,
          source: "capture",
          caption: "flow",
        }}
        sessionId="sess-1"
        client={client}
        entryKey="visual:art-strip"
        toolOutput={toolOutput}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("transcript-visual-filmstrip")).toBeTruthy();
    });
    await waitFor(() => {
      expect(client.getSessionArtifact).toHaveBeenCalledWith(
        "sess-1",
        "art-strip",
      );
    });

    const reading = await waitFor(() =>
      screen.getByTestId("transcript-visual-filmstrip-reading"),
    );
    expect(reading.textContent).toContain("Navigate idle");
    expect(reading.textContent).toContain("page#1");

    const steps = await waitFor(() => {
      const btns = screen.getAllByRole("option");
      expect(btns.length).toBe(2);
      return btns;
    });
    expect(at(steps, 0).getAttribute("aria-label")).toBe("initial");
    expect(at(steps, 1).getAttribute("aria-label")).toBe("click #load");
    fireEvent.click(at(steps, 1));
    expect(at(steps, 1).getAttribute("aria-selected")).toBe("true");
    await waitFor(() => {
      expect(
        screen.getByTestId("transcript-visual-filmstrip-reading").textContent,
      ).toContain("click · #load");
    });
  });
});

describe("TranscriptVisualArtifact filmstrip refused fetch", () => {
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
          artifact={{ id: "art-gone", mime: FILMSTRIP_MIME, source: "capture" }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-gone"
        />
      </ErrorBoundary>
    ));

    await screen.findByTestId("transcript-visual-filmstrip-error");
    expect(stageFailed).not.toHaveBeenCalled();
    expect(screen.queryByTestId("stage-boundary")).toBeNull();
    expect(screen.getByTestId("transcript-visual-filmstrip")).toBeTruthy();
  });
});
