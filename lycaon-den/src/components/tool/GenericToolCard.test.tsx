import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { GenericToolCard } from "./GenericToolCard.tsx";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";

const openFilesSurface = vi.hoisted(() => vi.fn());
const openInSearch = vi.hoisted(() => vi.fn());
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface }));
vi.mock("../../search/search-nav.ts", () => ({ openInSearch }));
beforeEach(() => { openFilesSurface.mockReset(); openInSearch.mockReset(); });

function readPart(
  overrides: Partial<ToolPartView> = {},
): ToolPartView {
  return {
    id: "r1",
    toolCallId: "r1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "read",
    kind: "read",
    status: "completed",
    title: "main.go",
    args: { path: "main.go" },
    output: "package main\n\nfunc main() {}",
    error: null,
    ...overrides,
  };
}

describe("GenericToolCard", () => {
  it("renders completed reads collapsed by default", () => {
    const { container } = render(() => (
      <GenericToolCard part={readPart()} layout="chat" />
    ));
    const details = container.querySelector(
      'details.den-tool-part[data-tool="read"]',
    );
    expect(details).toBeTruthy();
    expect(details?.hasAttribute("open")).toBe(false);
  });

  it("renders running reads collapsed by default", () => {
    const { container } = render(() => (
      <GenericToolCard
        part={readPart({ status: "running", output: null })}
        layout="chat"
      />
    ));
    const details = container.querySelector(
      'details.den-tool-part[data-tool="read"]',
    );
    expect(details?.hasAttribute("open")).toBe(false);
  });

  it("renders failed reads collapsed by default", () => {
    const { container } = render(() => (
      <GenericToolCard
        part={readPart({
          status: "error",
          error: "approval denied",
          output: "approval denied",
        })}
        layout="chat"
      />
    ));
    const details = container.querySelector(
      'details.den-tool-part[data-tool="read"]',
    );
    expect(details?.hasAttribute("open")).toBe(false);
  });

  it("renders completed reads collapsed in worker layout", () => {
    const { container } = render(() => (
      <GenericToolCard part={readPart()} layout="worker" />
    ));
    const details = container.querySelector(
      'details.den-tool-part[data-tool="read"][data-layout="worker"]',
    );
    expect(details?.hasAttribute("open")).toBe(false);
  });

  it("renders generic tools collapsed by default", () => {
    const { container } = render(() => (
      <GenericToolCard
        part={{
          id: "u1",
          toolCallId: "u1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "update_progress",
          kind: "generic",
          status: "completed",
          title: "- [ ] Wire login form\n- [x] Add tests",
          args: { content: "- [ ] Wire login form\n- [x] Add tests" },
          output: JSON.stringify({ status: "updated" }),
          error: null,
        }}
        layout="chat"
      />
    ));
    const details = container.querySelector(
      'details.den-tool-part[data-tool="update_progress"]',
    ) as HTMLDetailsElement | null;
    expect(details?.hasAttribute("open")).toBe(false);
  });

  it("renders a typed lifecycle completion in the card summary", () => {
    const { getByText } = render(() => (
      <GenericToolCard
        part={readPart({
          completion: { operation: "overlay_promotion", state: "promoted" },
        })}
        layout="chat"
      />
    ));
    expect(getByText("overlay promotion · promoted")).toBeTruthy();
  });

  it("opens generic tool body on summary click", async () => {
    const { container } = render(() => (
      <GenericToolCard
        part={{
          id: "u1",
          toolCallId: "u1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "update_progress",
          kind: "generic",
          status: "completed",
          args: { content: "- [ ] Wire login form\n- [x] Add tests" },
          output: JSON.stringify({ status: "updated" }),
          error: null,
        }}
        layout="chat"
      />
    ));
    const details = container.querySelector(
      'details.den-tool-part[data-tool="update_progress"]',
    );
    if (!(details instanceof HTMLDetailsElement)) throw new Error("Missing progress details");
    details.open = true;
    fireEvent(details, new Event("toggle", { bubbles: true }));
    await Promise.resolve();
    expect(details.open).toBe(true);
    expect(details?.querySelector(".den-tool-part-card-structured")).toBeTruthy();
  });

  it("opens complete raw output in Files and searches only related calls", async () => {
    const { container, getByRole } = render(() => (
      <GenericToolCard
        part={readPart({
          tool: "find",
          kind: "generic",
          output: JSON.stringify({
            results: [{ path: "src/main.go", type: "file" }],
            truncated: false,
          }),
        })}
        layout="chat"
        sessionId="sess-1"
        projectId="proj-1"
      />
    ));
    const card = container.querySelector(
      'details.den-tool-part[data-tool="find"]',
    );
    if (!(card instanceof HTMLDetailsElement)) throw new Error("Missing find details");
    card.open = true;
    fireEvent(card, new Event("toggle", { bubbles: true }));
    await Promise.resolve();
    expect(getByRole("button", { name: "Results in Files" })).toBeTruthy();
    expect(
      card?.querySelector('[data-testid="tool-related-search-link"]'),
    ).toBeTruthy();
    expect(
      card?.querySelector('[data-testid="tool-raw-add-to-chat"]'),
    ).toBeTruthy();
    expect(card?.querySelector('[data-testid="tool-raw-output"]')).toBeNull();
    expect(card?.textContent).not.toContain("View raw output in search");
    fireEvent.click(getByRole("button", { name: "Raw output in Files" }));
    expect(openFilesSurface).toHaveBeenCalledWith(expect.objectContaining({
      kind: "chat-content", projectId: "proj-1", document: expect.objectContaining({
        sessionId: "sess-1", messageId: "m1", toolCallId: "r1", pane: "output",
        content: { kind: "inline", redaction: undefined,
          text: JSON.stringify({ results: [{ path: "src/main.go", type: "file" }], truncated: false }) },
      }),
    }));
    expect(openInSearch).not.toHaveBeenCalled();
    fireEvent.click(getByRole("button", { name: "Find related tool calls" }));
    expect(openInSearch).toHaveBeenCalledWith("proj-1", "session:sess-1 tool:find kind:tool NOT ref:r1");
  });

  it("opens retained raw output through its content reference instead of its preview", async () => {
    const reference = { field: "tool_output" as const, sha256: "hash", total_runes: 20000, size_bytes: 20000, rows: 500 };
    const { container, getByRole } = render(() => <GenericToolCard
      part={readPart({ output: "bounded preview", outputReference: reference })}
      layout="worker" sessionId="worker-session" projectId="proj-1" />);
    const card = container.querySelector("details")!;
    card.open = true;
    fireEvent(card, new Event("toggle", { bubbles: true }));
    await Promise.resolve();
    fireEvent.click(getByRole("button", { name: "Raw output in Files" }));
    expect(openFilesSurface).toHaveBeenCalledWith(expect.objectContaining({ document: expect.objectContaining({
      sessionId: "worker-session", messageId: "m1", pane: "output", content: { kind: "retained", reference },
    }) }));
  });

  it("does not offer an ungrounded raw-output attachment", async () => {
    const { container } = render(() => (
      <GenericToolCard
        part={readPart({
          toolCallId: undefined,
          tool: "find",
          kind: "generic",
          output: JSON.stringify({ results: [] }),
        })}
        layout="chat"
        sessionId="sess-1"
        projectId="proj-1"
      />
    ));
    const card = container.querySelector(
      'details.den-tool-part[data-tool="find"]',
    );
    if (!(card instanceof HTMLDetailsElement)) throw new Error("Missing find details");
    card.open = true;
    fireEvent(card, new Event("toggle", { bubbles: true }));
    await Promise.resolve();
    expect(
      card?.querySelector('[data-testid="tool-raw-add-to-chat"]'),
    ).toBeNull();
  });

  describe("capture visuals", () => {
    const snapshotPart = () =>
      readPart({
        id: "cap1",
        toolCallId: "cap1",
        tool: "page_snapshot",
        kind: "generic",
        title: "Live reloader status UI",
        args: { id: "page-1", caption: "Live reloader status UI" },
        output: '{"final_url":"http://127.0.0.1:8765/"}',
        visual: {
          id: "artifact-1",
          mime: "image/png",
          source: "capture",
          caption: "Live reloader status UI",
        },
      });

    it("keeps the capture inside the row disclosure, not beside it", async () => {
      const { container } = render(() => (
        <GenericToolCard
          part={snapshotPart()}
          layout="chat"
          sessionId="sess-1"
          projectId="proj-1"
        />
      ));
      const details = container.querySelector(
        'details.den-tool-part[data-tool="page_snapshot"]',
      );
      expect(details).toBeTruthy();
      (details as HTMLDetailsElement).open = true;
      fireEvent(details!, new Event("toggle", { bubbles: true }));
      await Promise.resolve();
      const body = details?.querySelector('[data-testid="tool-part-body"]');
      expect(body?.querySelector('[data-testid="transcript-visual-artifact"]'))
        .toBeTruthy();
      expect(container.firstElementChild).toBe(details);
    });

    it("does not open the row just because a capture landed", () => {
      const { container } = render(() => (
        <GenericToolCard
          part={snapshotPart()}
          layout="chat"
          sessionId="sess-1"
          projectId="proj-1"
        />
      ));
      const details = container.querySelector(
        'details.den-tool-part[data-tool="page_snapshot"]',
      ) as HTMLDetailsElement | null;
      expect(details?.hasAttribute("open")).toBe(false);
    });
  });
});

it("leads with the command and retains its exact handle in the disclosure", async () => {
  const handle = "7441efdd-29d7-4cd9-899e-3c542d62d8ba";
  const { container, findByText } = render(() => <GenericToolCard layout="chat" part={readPart({
    tool: "command_output", kind: "generic", title: "./task den:test:fast", args: { handle, cursor: 0 }, output: "done",
  })} />);
  const summary = container.querySelector("summary");
  if (!summary) throw new Error("Missing command summary");
  expect(summary.textContent).toContain("./task den:test:fast");
  expect(summary.textContent).not.toContain(handle);
  expect(summary.getAttribute("aria-label")).toContain("./task den:test:fast");
  fireEvent.click(summary);
  expect(await findByText(handle)).toBeTruthy();
});
