import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { SearchHit } from "../../api/types.ts";
import { SearchHitDetail } from "./SearchHitDetail.tsx";

const openSourceLocation = vi.hoisted(() => vi.fn());
const confirmAndOpenExternalLink = vi.hoisted(() => vi.fn());
const addToChat = vi.hoisted(() => vi.fn());
const loadSearchToolContent = vi.hoisted(() => vi.fn());
const openFilesSurface = vi.hoisted(() => vi.fn());

vi.mock("../../search/search-tool-content.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../search/search-tool-content.ts")>(),
  loadSearchToolContent,
}));
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface }));
vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../platform/connection/app-connection.ts")>(),
  getLycaonClient: () => ({}),
}));

vi.mock("../../platform/navigation/open-source.ts", () => ({
  openSourceLocation,
}));

vi.mock("../../platform/desktop/external-link.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/desktop/external-link.ts")>();
  return {
    ...actual,
    confirmAndOpenExternalLink,
  };
});

vi.mock("../../chat/composer/add-to-chat.ts", () => ({
  addToChat: (...args: unknown[]) => addToChat(...args),
}));

function renderDetail(
  hit: Omit<SearchHit, "hit_id"> & { hit_id?: string },
  overrides?: {
    onNavigate?: () => void;
    onPivot?: (field: string, value: string) => void;
    onClose?: () => void;
    pivots?: { label: string; field: string; value: string }[];
    rootRefs?: { id: string; path: string }[];
  },
) {
  return render(() => (
    <SearchHitDetail
      hit={{
        hit_id: "detail-test-hit",
        ...hit,
      }}
      pivots={overrides?.pivots ?? []}
      rootRefs={overrides?.rootRefs}
      onPivot={overrides?.onPivot ?? (() => undefined)}
      onNavigate={overrides?.onNavigate ?? (() => undefined)}
      onClose={overrides?.onClose ?? (() => undefined)}
    />
  ));
}

describe("SearchHitDetail", () => {
  beforeEach(() => {
    openSourceLocation.mockReset();
    confirmAndOpenExternalLink.mockReset();
    confirmAndOpenExternalLink.mockResolvedValue(true);
    addToChat.mockReset();
    loadSearchToolContent.mockReset();
    openFilesSurface.mockReset();
  });

  it("shows a readable preview without an inner scroll cap", () => {
    renderDetail({
      hit_kind: "evidence",
      source: "message",
      project_id: "proj-a",
      session_id: "sess-a",
      source_ref: "read#1",
      title: "fn main()",
      snippet: "fn main()\nlet x = 1;",
      score: 1,
    });
    const preview = screen.getByTestId("search-result-preview");
    expect(preview.textContent).toContain("let x = 1");
    expect(screen.getByTestId("search-result-open").textContent).toBe(
      "Open in chat",
    );
  });

  it("shows a summary instead of raw tool JSON", () => {
    renderDetail({
      hit_kind: "tool",
      source: "message",
      project_id: "proj-a",
      source_ref: "call_1",
      title: "read",
      snippet: 'read {"path":"src/main.go"}',
      score: 1,
    });
    const preview = screen.getByTestId("search-result-preview");
    expect(preview.getAttribute("data-format")).toBe("prose");
    expect(preview.textContent).toBe("Recorded input to read.");
    expect(screen.queryByTestId("search-tool-content-open")).toBeNull();
  });

  it("opens recorded output in a tool Files tab and reports unavailable content", async () => {
    const document = { sessionId: "worker-session", messageId: "result", toolCallId: "call_1", pane: "output" };
    loadSearchToolContent.mockRejectedValueOnce(new Error("Content unavailable"));
    loadSearchToolContent.mockResolvedValueOnce(document);
    renderDetail({ hit_kind: "tool", source: "tool", project_id: "proj-a",
      session_id: "worker-session", parent_session_id: "parent-session", worker_id: "worker",
      source_ref: "call_1", title: "summarize", snippet: "partial JSON" });
    const link = screen.getByRole("button", { name: "Output in Files" });
    fireEvent.click(link);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("Content unavailable"));
    expect(openFilesSurface).not.toHaveBeenCalled();
    fireEvent.click(link);
    await waitFor(() => expect(openFilesSurface).toHaveBeenCalledWith({ kind: "chat-content", projectId: "proj-a", document }));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("does not navigate to a previously selected result after its request finishes", async () => {
    let finish: (document: unknown) => void = () => undefined;
    loadSearchToolContent.mockImplementation(() => new Promise(resolve => { finish = resolve; }));
    const [hit, setHit] = createSignal<SearchHit>({ hit_id: "first", hit_kind: "tool", source: "tool",
      project_id: "proj-a", session_id: "sess-a", source_ref: "call_1", title: "read" });
    render(() => <SearchHitDetail hit={hit()} pivots={[]} onPivot={() => {}} onNavigate={() => {}} onClose={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Output in Files" }));
    const signal = loadSearchToolContent.mock.calls[0]?.[2] as AbortSignal;
    setHit({ ...hit(), score: 2 });
    expect(signal.aborted).toBe(false);
    expect(screen.getByRole("button", { name: "Opening…" }).hasAttribute("disabled")).toBe(true);
    setHit({ ...hit(), hit_id: "second", source_ref: "call_2", source: "message" });
    expect(signal.aborted).toBe(true);
    finish({ messageId: "old-result" });
    await waitFor(() => expect(screen.getByRole("button", { name: "Input in Files" }).hasAttribute("disabled")).toBe(false));
    expect(openFilesSurface).not.toHaveBeenCalled();
  });

  it("opens a code hit's file as a preview buffer", async () => {
    renderDetail({
      hit_kind: "code",
      source: "code",
      project_id: "proj-a",
      source_ref: "pkg/a.go:12",
      path: "pkg/a.go",
      line: 12,
      title: "func Foo() {}",
      snippet: "func Foo() {}",
      score: 1,
    });
    fireEvent.click(screen.getByTestId("search-result-open"));
    expect(openSourceLocation).toHaveBeenCalledWith({
      intent: "transient",
      projectId: "proj-a",
      path: "pkg/a.go",
      entryKind: "file",
      line: 12,
      focus: true,
    });
  });

  it("gives chat-first hits with a host path a second Open file action", async () => {
    const onNavigate = vi.fn();
    renderDetail(
      {
        hit_kind: "evidence",
        source: "message",
        project_id: "proj-a",
        session_id: "sess-a",
        source_ref: "AGENTS.md",
        path: "AGENTS.md",
        title: "Agent policy",
        snippet: "# Lycaon — agent policy",
        score: 1,
      },
      { onNavigate },
    );
    expect(screen.getByTestId("search-result-open").textContent).toBe(
      "Open in chat",
    );
    fireEvent.click(screen.getByTestId("search-detail-open-file"));
    expect(openSourceLocation).toHaveBeenCalledWith({
      intent: "permanent",
      projectId: "proj-a",
      path: "AGENTS.md",
      line: undefined,
      focus: true,
    });
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("opens web URLs through the external-link confirm gate", async () => {
    renderDetail({
      hit_kind: "web",
      source: "web",
      project_id: "proj-a",
      title: "Docs",
      url: "https://example.com/a",
      snippet: JSON.stringify({ title: "Docs", url: "https://example.com/a" }),
      score: 1,
    });
    fireEvent.click(screen.getByTestId("search-result-open"));
    expect(confirmAndOpenExternalLink).toHaveBeenCalledWith(
      "https://example.com/a",
    );
  });

  it("hosts the refine pivots and the close control", () => {
    const onPivot = vi.fn();
    const onClose = vi.fn();
    renderDetail(
      {
        hit_kind: "finding",
        source: "finding",
        project_id: "proj-a",
        source_ref: "scan-1",
        title: "Remote script piped to shell.",
        snippet: "Remote script piped to shell.",
        score: 1,
      },
      {
        onPivot,
        onClose,
        pivots: [{ label: "kind:finding", field: "kind", value: "finding" }],
      },
    );
    fireEvent.click(screen.getByTestId("search-pivot-kind"));
    expect(onPivot).toHaveBeenCalledWith("kind", "finding");
    fireEvent.click(screen.getByTestId("detail-close"));
    expect(onClose).toHaveBeenCalled();
  });

  it("Add to chat emits path-file for an in-jail code hit", async () => {
    renderDetail(
      {
        hit_kind: "code",
        source: "code",
        project_id: "proj-a",
        session_id: "sess-a",
        source_ref: "pkg/a.go:12",
        path: "pkg/a.go",
        line: 12,
        title: "func Foo() {}",
        snippet: "func Foo() {}",
        score: 1,
      },
      { rootRefs: [{ id: "root-a", path: "/proj" }] },
    );
    expect(screen.getByTestId("search-hit-detail").getAttribute("draggable"))
      .toBe("true");
    fireEvent.click(screen.getByTestId("search-result-add-to-chat"));
    expect(addToChat).toHaveBeenCalledWith({
      kind: "path-file",
      projectId: "proj-a",
      rootId: "root-a",
      path: "pkg/a.go",
      name: "a.go",
    });
  });

  it("Add to chat emits search-hit for a finding without a path", async () => {
    renderDetail({
      hit_kind: "finding",
      source: "finding",
      project_id: "proj-a",
      session_id: "sess-a",
      source_ref: "scan-1",
      title: "Remote script",
      snippet: "piped",
      score: 1,
    });
    expect(screen.getByTestId("search-hit-detail").getAttribute("draggable"))
      .toBe("true");
    fireEvent.click(screen.getByTestId("search-result-add-to-chat"));
    expect(addToChat).toHaveBeenCalledWith({
      kind: "search-hit",
      projectId: "proj-a",
      sessionId: "sess-a",
      sourceRef: "scan-1",
      hitKind: "finding",
      name: "Remote script",
    });
  });

  it("hides Add to chat when the hit cannot form a ref", () => {
    renderDetail({
      hit_kind: "message",
      source: "message",
      project_id: "proj-a",
      session_id: "sess-a",
      title: "hello",
      snippet: "hello",
      score: 1,
    });
    expect(screen.queryByTestId("search-result-add-to-chat")).toBeNull();
    expect(screen.getByTestId("search-hit-detail").getAttribute("draggable"))
      .not.toBe("true");
  });
});
