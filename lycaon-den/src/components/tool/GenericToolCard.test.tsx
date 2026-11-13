import { assertNamedControl } from "../../test/controls.ts";
import { disclosureToggle, isDisclosureOpen, rowSpokenLabel, rowVisibleText } from "../../test/transcript-disclosure-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { GenericToolCard } from "./GenericToolCard.tsx";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";

const openFilesSurface = vi.hoisted(() => vi.fn());
const openInSearch = vi.hoisted(() => vi.fn());
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface }));
vi.mock("../../search/search-nav.ts", () => ({ openInSearch }));
vi.mock("../../utils/clipboard.ts", () => ({ writeClipboardText: vi.fn().mockResolvedValue(undefined) }));
beforeEach(() => { openFilesSurface.mockReset(); openInSearch.mockReset(); vi.mocked(writeClipboardText).mockClear(); });

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
      '.den-tool-part[data-transcript-disclosure][data-tool="read"]',
    );
    expect(details).toBeTruthy();
    expect(details?.getAttribute("data-expanded") === "true").toBe(false);
  });

  it("renders running reads collapsed by default", () => {
    const { container } = render(() => (
      <GenericToolCard
        part={readPart({ status: "running", output: null })}
        layout="chat"
      />
    ));
    const details = container.querySelector(
      '.den-tool-part[data-transcript-disclosure][data-tool="read"]',
    );
    expect(details?.getAttribute("data-expanded") === "true").toBe(false);
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
      '.den-tool-part[data-transcript-disclosure][data-tool="read"]',
    );
    expect(details?.getAttribute("data-expanded") === "true").toBe(false);
  });

  it("renders completed reads collapsed in worker layout", () => {
    const { container } = render(() => (
      <GenericToolCard part={readPart()} layout="worker" />
    ));
    const details = container.querySelector(
      '.den-tool-part[data-transcript-disclosure][data-tool="read"][data-layout="worker"]',
    );
    expect(details?.getAttribute("data-expanded") === "true").toBe(false);
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
      '.den-tool-part[data-transcript-disclosure][data-tool="update_progress"]',
    ) as HTMLElement | null;
    expect(details?.getAttribute("data-expanded") === "true").toBe(false);
  });

  it("renders a typed lifecycle completion in the card summary", () => {
    const { getByTestId } = render(() => (
      <GenericToolCard
        part={readPart({
          completion: { operation: "overlay_promotion", state: "promoted" },
        })}
        layout="chat"
      />
    ));
    const completion = getByTestId("tool-part-completion");
    expect(completion.textContent).toContain("overlay promotion");
    expect(completion.textContent).toContain("promoted");
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
      '.den-tool-part[data-transcript-disclosure][data-tool="update_progress"]',
    );
    if (!(details instanceof HTMLElement)) throw new Error("Missing progress details");
    fireEvent.click(disclosureToggle(details));
    await Promise.resolve();
    expect(isDisclosureOpen(details)).toBe(true);
    expect(details?.querySelector(".den-tool-part-card-structured")).toBeTruthy();
  });

  it("opens complete raw output in Files and searches only related calls", async () => {
    const { container } = render(() => (
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
      '.den-tool-part[data-transcript-disclosure][data-tool="find"]',
    );
    if (!(card instanceof HTMLElement)) throw new Error("Missing find details");
    fireEvent.click(disclosureToggle(card));
    await Promise.resolve();
    expect(assertNamedControl(screen.getByTestId("tool-content-link"), "button")).toBeTruthy();
    expect(
      card?.querySelector('[data-testid="tool-related-search-link"]'),
    ).toBeTruthy();
    expect(
      card?.querySelector('[data-testid="tool-raw-add-to-chat"]'),
    ).toBeTruthy();
    expect(card?.querySelector('[data-testid="tool-raw-output"]')).toBeNull();
    expect(card?.textContent).not.toContain("View raw output in search");
    fireEvent.click(assertNamedControl(screen.getByTestId("tool-raw-output-link"), "button"));
    expect(openFilesSurface).toHaveBeenCalledWith(expect.objectContaining({
      kind: "chat-content", projectId: "proj-1", document: expect.objectContaining({
        sessionId: "sess-1", messageId: "m1", toolCallId: "r1", pane: "output",
        content: { kind: "inline", redaction: undefined,
          text: JSON.stringify({ results: [{ path: "src/main.go", type: "file" }], truncated: false }) },
      }),
    }));
    expect(openInSearch).not.toHaveBeenCalled();
    fireEvent.click(assertNamedControl(screen.getByTestId("tool-related-search-link"), "button"));
    expect(openInSearch).toHaveBeenCalledWith("proj-1", "session:sess-1 tool:find kind:tool NOT ref:r1");
  });

  it("opens retained raw output through its content reference instead of its preview", async () => {
    const reference = { field: "tool_output" as const, sha256: "hash", total_runes: 20000, size_bytes: 20000, rows: 500 };
    const { container } = render(() => <GenericToolCard
      part={readPart({ output: "bounded preview", outputReference: reference })}
      layout="worker" sessionId="worker-session" projectId="proj-1" />);
    const card = container.querySelector<HTMLElement>("[data-transcript-disclosure]")!;
    fireEvent.click(disclosureToggle(card));
    await Promise.resolve();
    fireEvent.click(assertNamedControl(screen.getByTestId("tool-raw-output-link"), "button"));
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
      '.den-tool-part[data-transcript-disclosure][data-tool="find"]',
    );
    if (!(card instanceof HTMLElement)) throw new Error("Missing find details");
    fireEvent.click(disclosureToggle(card));
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
        '.den-tool-part[data-transcript-disclosure][data-tool="page_snapshot"]',
      );
      expect(details).toBeTruthy();
      fireEvent.click(disclosureToggle(details));
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
        '.den-tool-part[data-transcript-disclosure][data-tool="page_snapshot"]',
      ) as HTMLElement | null;
      expect(details?.getAttribute("data-expanded") === "true").toBe(false);
    });
  });
});

it("names its target in the spoken label and leads the opened details with it", async () => {
  const handle = "7441efdd-29d7-4cd9-899e-3c542d62d8ba";
  const { container, findByText } = render(() => <GenericToolCard layout="chat" part={readPart({
    tool: "command_output", kind: "generic", title: "./task den:test:fast", args: { handle, cursor: 0 }, output: "done",
  })} />);
  const card = container.querySelector('[data-testid="tool-part-card"]');
  expect(rowVisibleText(card)).not.toContain("./task den:test:fast");
  expect(rowVisibleText(card)).not.toContain(handle);
  expect(rowSpokenLabel(card)).toContain("./task den:test:fast");
  fireEvent.click(disclosureToggle(card));
  expect(container.querySelector('[data-testid="activity-target"] code')?.textContent).toBe("./task den:test:fast");
  expect(await findByText(handle)).toBeTruthy();
});

it("orders an opened card target, briefing, body, then records", () => {
  const slots = { purpose: <span>supplied purpose</span>, result: <span>supplied result</span>, evidence: <button type="button">supplied evidence</button> };
  const { container } = render(() => <GenericToolCard layout="chat" part={readPart()} slots={slots} />);
  expect(container.querySelector('[data-testid="activity-purpose"]')).toBeNull();
  fireEvent.click(disclosureToggle(container.querySelector('[data-testid="tool-part-card"]')));
  const body = container.querySelector('[data-testid="tool-part-body"]')!;
  const order = [...body.querySelectorAll('[data-testid="activity-target"] code, [data-testid="activity-purpose"], [data-testid="activity-result"], .den-tool-part-card-structured, nav')]
    .map(node => node.matches(".den-tool-part-card-structured") ? "body" : node.textContent);
  expect(order).toEqual(["main.go", "supplied purpose", "supplied result", "body", "supplied evidence"]);
});

describe("a command call without a program", () => {
  const source = "mkdir -p mud web && cat > mud/world.py <<'EOF'\nROOMS = {}\nEOF";
  const commandPart = (overrides: Partial<ToolPartView> = {}) => readPart({ tool: "command", kind: "command", title: undefined, status: "error", outcome: "rejected", output: "Code: COMMAND_REFUSED", args: { command: source, cwd: "/workspace/project" }, ...overrides });

  it("shows the command once, first, with its working directory kept in the input", () => {
    const { container } = render(() => <GenericToolCard layout="chat" part={commandPart()} />);
    fireEvent.click(disclosureToggle(container.querySelector('[data-testid="tool-part-card"]')));
    expect(container.querySelector('[data-testid="activity-target"] code')?.textContent).toBe(source);
    const input = container.querySelector(".den-tool-part-card-structured")?.textContent ?? "";
    expect(input).not.toContain(source);
    expect(input).toContain("/workspace/project");
  });

  it("marks the host's replacements inside the command", () => {
    const redacted = "curl -H 'token: [REDACTED]' example.test";
    const meta = { spans: [{ field: "tool_calls.0.args.command", start: redacted.indexOf("[REDACTED]"), length: "[REDACTED]".length, kind: "secret" as const }] };
    const { container } = render(() => <GenericToolCard layout="chat" part={commandPart({ args: { command: redacted }, argsRedaction: { meta, fieldPrefix: "tool_calls.0.args" } })} />);
    fireEvent.click(disclosureToggle(container.querySelector('[data-testid="tool-part-card"]')));
    expect(container.querySelector('[data-testid="activity-target"] .den-redaction-mark')?.textContent).toBe("[REDACTED]");
  });

  it("copies the whole command", async () => {
    const { container } = render(() => <GenericToolCard layout="chat" part={commandPart()} />);
    fireEvent.click(disclosureToggle(container.querySelector('[data-testid="tool-part-card"]')));
    fireEvent.click(assertNamedControl(screen.getByTestId("activity-target-copy"), "button"));
    await vi.waitFor(() => expect(writeClipboardText).toHaveBeenCalledWith(source));
  });
});

it("keeps a path in the input facts beside the target", () => {
  const { container } = render(() => <GenericToolCard layout="chat" part={readPart()} projectId="project" />);
  fireEvent.click(disclosureToggle(container.querySelector('[data-testid="tool-part-card"]')));
  expect(container.querySelector('[data-testid="activity-target"] code')?.textContent).toBe("main.go");
  expect(container.querySelector(".den-tool-part-card-structured")?.textContent).toContain("main.go");
});
