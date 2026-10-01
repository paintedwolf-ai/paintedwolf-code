import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { FileMarkdownPreview } from "./FileMarkdownPreview.tsx";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { MarkdownPreviewResponse } from "../../chat/markdown/markdown-preview-document.ts";

class PreviewWorker {
  static instances: PreviewWorker[] = [];
  onmessage?: (event: MessageEvent<MarkdownPreviewResponse>) => void;
  onerror?: () => void;
  postMessage = vi.fn();
  terminate = vi.fn();
  constructor() { PreviewWorker.instances.push(this); }
  reply(message: MarkdownPreviewResponse) { this.onmessage?.({ data: message } as MessageEvent<MarkdownPreviewResponse>); }
}

afterEach(() => { registerNoticePublisher(null); vi.unstubAllGlobals(); PreviewWorker.instances = []; });
const largeSource = "# Heading\n\nText.\n\n".repeat(4000);

describe("large file preview lifecycle", () => {
  it("paints before starting work and cancels stale, idle and disposed preparation", async () => {
    vi.stubGlobal("Worker", PreviewWorker);
    const [source, setSource] = createSignal(largeSource);
    const [active, setActive] = createSignal(true);
    const view = render(() => <ResidentPresenceProvider presence={active() ? "active" : "idle"}>
      <FileMarkdownPreview source={source()} projectId="project" />
    </ResidentPresenceProvider>);
    expect(PreviewWorker.instances).toHaveLength(0);
    expect(screen.getByTestId("files-editor-preview")).toBeTruthy();
    await waitFor(() => expect(PreviewWorker.instances).toHaveLength(1));
    const first = PreviewWorker.instances[0]!;
    expect(first.postMessage).toHaveBeenCalledWith({ type: "prepare", source: largeSource });
    setSource(largeSource + "New source");
    expect(first.terminate).toHaveBeenCalledOnce();
    first.reply({ type: "error" });
    expect(screen.queryByRole("alert")).toBeNull();
    await waitFor(() => expect(PreviewWorker.instances).toHaveLength(2));
    setActive(false);
    expect(PreviewWorker.instances[1]!.terminate).toHaveBeenCalledOnce();
    setActive(true);
    await waitFor(() => expect(PreviewWorker.instances).toHaveLength(3));
    view.unmount();
    expect(PreviewWorker.instances[2]!.terminate).toHaveBeenCalledOnce();
  });

  it("reports worker failure as a notice without synchronous parsing", async () => {
    vi.stubGlobal("Worker", PreviewWorker);
    const store = createNoticeStore();
    registerNoticePublisher(store);
    render(() => <FileMarkdownPreview source={largeSource} projectId="project" />);
    await waitFor(() => expect(PreviewWorker.instances).toHaveLength(1));
    PreviewWorker.instances[0]!.reply({ type: "error" });
    await waitFor(() => {
      const rows = selectProjectNoticeGroups(store.index()).find(group => group.projectId === "project")?.notices ?? [];
      expect(rows).toMatchObject([{ code: "files_markdown_preview_unavailable" }]);
    });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("uses the shared preparation feedback after the loading grace period", async () => {
    vi.stubGlobal("Worker", PreviewWorker);
    render(() => <FileMarkdownPreview source={largeSource} projectId="project" />);
    expect(screen.queryByRole("status")).toBeNull();
    const waiting = await screen.findByRole("status");
    expect(waiting.classList.contains("den-presentation-wait")).toBe(true);
    expect(waiting.textContent).toBe("Preparing preview…");
  });
});
