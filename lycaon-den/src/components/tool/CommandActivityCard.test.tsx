import { assertNamedControl } from "../../test/controls.ts";
import { beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import type { CommandActivity, CommandActivityStep, CommandExecutionReceipt, CommandProgramReceipt } from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { disclosureToggle } from "../../test/transcript-disclosure-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { clearTranscriptEntryMemory } from "../../chat/transcript/presentation/transcript-entry.ts";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../../platform/navigation/open-files-surface.ts";
import { CommandActivityCard } from "./CommandActivityCard.tsx";
import { writeClipboardText } from "../../utils/clipboard.ts";
vi.mock("../../utils/clipboard.ts", () => ({ writeClipboardText: vi.fn().mockResolvedValue(undefined) }));

const source = "cd /project && cargo build --release | tail -2 && cargo test | head -3";
const program: CommandProgramReceipt = { id: "program", parent_receipt_id: "receipt", language_version: 1, catalog_digest: "catalog", source_digest: "digest", source_ref: "source", state: "completed", progress_state: "unchanged", detail_ref: "detail", started_at: "2026-10-30T10:00:00Z" };
const baseFact: CommandActivity = { id: "test", group: "Running programs", phase: "execute", label: "Run process", subject: "cargo test", state: "completed", outcome: "Exited 0", status_code: 0, execution_ids: ["test"], spans: [{ source_ref: "source", start_byte: source.indexOf("cargo test"), end_byte: source.length }] };

function part(fact: Partial<CommandActivity> = {}): ToolPartView {
  const original: ToolPartView = { id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call", tool: "command", kind: "command", status: "completed", args: { command: source }, output: "agent-visible output", commandProgram: program };
  const merged = { ...baseFact, ...fact };
  return { ...original, commandActivity: { fact: merged, members: [merged], calls: [original], programIds: [program.id] } };
}
function receipt(extra: Partial<CommandExecutionReceipt> = {}): CommandExecutionReceipt {
  return { id: "test", program_id: "program", node_id: "node", parent_execution_id: "pipeline", owner: "processes", lifecycle: "effect_attempt", definition_digest: "definition", arguments_digest: "args", arguments_ref: "args", command: "cargo test", state: "completed", owner_started: true, owner_invoked: true, effect_certainty: "none", complete: true, publication_eligible: false, output_publishable: true, detail_ref: "detail", status_code: 0, outputs: [{ stream: "stdout", ref: "out", size_bytes: 11, complete: true, redacted: false, preview: "test output", preview_truncated: false, tail_preview: "", line_count: 1 }], ...extra };
}
function page(executions: CommandExecutionReceipt[] = [receipt()]) {
  return { program, executions, nodes: [], executions_omitted: 0, nodes_omitted: 0 };
}
async function opened(value: ToolPartView, get = vi.fn().mockResolvedValue(page())) {
  const client = stubClient({ getCommandProgramReceipt: get });
  const view = render(() => <CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" projectId="project" client={client} />);
  fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
  await waitFor(() => expect(view.container.querySelector('[data-testid="command-activity-details"]')).toBeTruthy());
  return { view, get };
}
const region = (container: HTMLElement, id: string) => container.querySelector(`[data-testid="${id}"]`);
function filesSink() {
  onTestFinished(resetOpenFilesSurfaceForTests);
  const sink = vi.fn(); registerOpenFilesSurfaceSink(sink);
  return sink;
}

beforeEach(() => { clearTranscriptEntryMemory("activity-card-test"); vi.clearAllMocks(); });

describe("activity detail layout", () => {
  it("loads receipts only after the card opens", async () => {
    const get = vi.fn().mockResolvedValue(page());
    const stub = stubClient({ getCommandProgramReceipt: get });
    const view = render(() => <CommandActivityCard part={part()} layout="chat" sessionId="activity-card-test" client={stub} />);
    expect(get).not.toHaveBeenCalled();
    fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
    await waitFor(() => expect(get).toHaveBeenCalledOnce());
    expect(get).toHaveBeenCalledWith("activity-card-test", "program", { limit: 6, cursor: undefined, activity_id: "test" });
  });

  it("renders only the regions a clean success has", async () => {
    const { view } = await opened(part({ phase: "mutate", execution_count: 1 }));
    await waitFor(() => expect(region(view.container, "activity-output")).toBeTruthy());
    expect(region(view.container, "activity-target")?.textContent).toContain(source);
    for (const absent of ["activity-result", "activity-steps", "activity-calls", "activity-exceptions", "activity-purpose"]) expect(region(view.container, absent)).toBeNull();
    expect(region(view.container, "activity-footer")).toBeTruthy();
  });

  it("shows a failure's result, its stderr tail and differing steps", async () => {
    const steps: CommandActivityStep[] = ["completed", "failed"].map((state, index) => ({ id: String(index), command: `step-${index}`, count: 1, running: 0, waiting: 0, failed: state === "failed" ? 1 : 0, refused: 0, interrupted: 0, state, outcome: state }));
    const failing = receipt({ state: "failed", status_code: 9, outputs: [{ stream: "stderr", ref: "err", size_bytes: 9000, complete: true, redacted: false, preview: "head of error", preview_truncated: true, tail_preview: "frame a\nframe b\nlast-supplied-line", line_count: 400 }] });
    const { view } = await opened(part({ state: "failed", status_code: 9, steps }), vi.fn().mockResolvedValue(page([failing])));
    await waitFor(() => expect(region(view.container, "activity-output")?.textContent).toContain("last-supplied-line"));
    expect(region(view.container, "activity-output")?.getAttribute("data-stream")).toBe("stderr");
    expect(region(view.container, "activity-output")?.textContent).not.toContain("head of error");
    expect(region(view.container, "activity-result")?.textContent).toContain("9");
    expect(region(view.container, "activity-steps")?.querySelectorAll(".den-activity-row")).toHaveLength(2);
  });

  it("fills the briefing slots in place of the deterministic result", async () => {
    const value = part({ state: "failed", status_code: 9 });
    const stub = stubClient({ getCommandProgramReceipt: vi.fn().mockResolvedValue(page()) });
    const view = render(() => <CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" projectId="project"
      client={stub}
      slots={{ purpose: <span>supplied purpose</span>, result: <span>supplied change</span>, evidence: <button type="button">supplied evidence</button> }} />);
    fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
    await waitFor(() => expect(region(view.container, "activity-purpose")?.textContent).toBe("supplied purpose"));
    expect(region(view.container, "activity-result")?.textContent).toBe("supplied change");
    expect(region(view.container, "activity-footer")?.textContent).toContain("supplied evidence");
    expect(view.container.querySelectorAll('[data-testid="activity-purpose"]')).toHaveLength(1);
  });

  it("shows and copies only the activity's own statements from a compound command", async () => {
    const heredoc = Array.from({ length: 12 }, (_, line) => `line ${line}`).join("\n");
    const compound = `cat > notes.txt <<'EOF'\n${heredoc}\nEOF\ncargo test`;
    const write = `cat > notes.txt <<'EOF'\n${heredoc}\nEOF`;
    const byteSpan = (needle: string) => {
      const start = new TextEncoder().encode(compound.slice(0, compound.indexOf(needle))).length;
      return { source_ref: "source", start_byte: start, end_byte: start + new TextEncoder().encode(needle).length };
    };
    const writing: CommandActivity = { ...baseFact, id: "write", label: "Write file", subject: "notes.txt", spans: [byteSpan(write)] };
    const testing: CommandActivity = { ...baseFact, spans: [byteSpan("cargo test")] };
    const receipt = { ...program, activities: [writing, testing] };
    const call: ToolPartView = { id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call", tool: "command", kind: "command", status: "completed", args: { command: compound }, commandProgram: receipt };
    const { view } = await opened({ ...call, commandActivity: { fact: writing, members: [writing], calls: [call], programIds: [program.id] } });
    expect(region(view.container, "activity-target")?.querySelector("code")?.textContent).toBe(write);
    fireEvent.click(view.getByTestId("activity-target-copy"));
    await waitFor(() => expect(writeClipboardText).toHaveBeenCalledWith(write));
  });

  it("counts further distinct commands folded into one activity", async () => {
    const calls = ["go test ./a", "go test ./b", "go test ./c"].map((command, index): ToolPartView => ({ id: `assistant:${index}`, assistantMessageId: "assistant", messageId: `result-${index}`, toolCallId: `call-${index}`, tool: "command", kind: "command", status: "completed", args: { command }, commandProgram: program }));
    const { view } = await opened({ ...calls[0]!, commandActivity: { fact: baseFact, members: [baseFact, baseFact, baseFact], calls, programIds: [program.id] } });
    expect(region(view.container, "activity-target")?.querySelector("code")?.textContent).toBe("go test ./a");
    expect(region(view.container, "activity-target")?.querySelector(".den-activity-target__more")?.textContent).toContain((calls.length - 1).toLocaleString());
  });

  it("copies the submitted command and announces the result", async () => {
    const { view } = await opened(part());
    fireEvent.click(assertNamedControl(view.getByTestId("activity-target-copy"), "button"));
    await waitFor(() => expect(writeClipboardText).toHaveBeenCalledWith(source));
    await waitFor(() => expect(view.getByRole("status").textContent).not.toBe(""));
  });

  it("announces a failed copy without claiming success", async () => {
    vi.mocked(writeClipboardText).mockRejectedValueOnce(new Error("denied"));
    const { view } = await opened(part());
    const copy = view.getByTestId("activity-target-copy");
    const label = copy.getAttribute("aria-label");
    fireEvent.click(copy);
    await waitFor(() => expect(view.getByRole("status").textContent).not.toBe(""));
    expect(copy.getAttribute("aria-label")).toBe(label);
  });

  it("keeps the recorded row when detail loading fails", async () => {
    const { view } = await opened(part(), vi.fn().mockRejectedValue(new Error("offline")));
    await waitFor(() => expect(view.container.querySelector(".den-activity-note")).toBeTruthy());
    expect(view.container.textContent).toContain("cargo test");
  });

  it("does not reload unchanged receipts when live snapshots replace their objects", async () => {
    const { createSignal } = await import("solid-js");
    const get = vi.fn().mockResolvedValue(page());
    const [value, setValue] = createSignal(part());
    const stub = stubClient({ getCommandProgramReceipt: get });
    const view = render(() => <CommandActivityCard part={value()} layout="chat" sessionId="activity-card-test" client={stub} />);
    fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
    await waitFor(() => expect(region(view.container, "command-activity-details")).toBeTruthy());
    const copy = view.getByTestId("activity-target-copy");
    const next = part(); next.commandActivity!.fact = { ...next.commandActivity!.fact };
    setValue(next);
    await Promise.resolve();
    expect(get).toHaveBeenCalledOnce();
    expect(view.getByTestId("activity-target-copy")).toBe(copy);
  });
});

describe("activity footer and records", () => {
  it("opens every program's receipts, including programs beyond the loaded pages", async () => {
    const sink = filesSink();
    const value = part(); const original = value.commandActivity!.calls[0]!;
    for (const id of ["p2", "p3", "p4", "p5"]) {
      value.commandActivity!.calls.push({ ...original, id, toolCallId: id, commandProgram: { ...program, id } });
      value.commandActivity!.members.push({ ...baseFact, id: `${id}:activity`, execution_ids: [`${id}:execution`] });
    }
    const get = vi.fn(async (_session: string, programId: string) => ({ ...page([{ ...receipt(), id: `${programId}:execution`, program_id: programId }]), program: { ...program, id: programId } }));
    const { view } = await opened(value, get);
    fireEvent.click(assertNamedControl(await view.findByTestId("command-activity-receipts"), "button"));
    expect(sink.mock.calls.at(-1)?.[0].document.content.programs.map((item: { id: string }) => item.id)).toEqual(["program", "p2", "p3", "p4", "p5"]);
    expect(get).toHaveBeenCalledTimes(3);
  });

  it("lists coalesced calls and reveals a later call through Find", async () => {
    const { createSignal } = await import("solid-js");
    const { bindFindableView } = await import("../../find/use-findable-view.ts");
    const { openFind, setFindQuery, findNext, findController, resetFindControllerForTests } = await import("../../find/find-controller.ts");
    onTestFinished(resetFindControllerForTests);
    const sink = filesSink();
    const value = part();
    value.commandActivity!.calls = Array.from({ length: 5 }, (_, index) => ({ ...value.commandActivity!.calls[0]!, id: `source-${index}`, toolCallId: `source-${index}`, args: { command: index === 4 ? "UNIQUE_FOLDED_NEEDLE" : `other ${index}` } }));
    const client = stubClient({ getCommandProgramReceipt: vi.fn().mockResolvedValue(page()) });
    render(() => {
      const [host, setHost] = createSignal<HTMLElement>();
      bindFindableView({ id: "session-transcript-activity-find", root: host, primary: true });
      return <div ref={setHost}><CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" projectId="project" client={client} /></div>;
    });
    openFind(); setFindQuery("UNIQUE_FOLDED_NEEDLE");
    await waitFor(() => expect(findController.matches().length).toBeGreaterThan(0));
    findNext();
    await waitFor(() => expect(document.querySelector('[data-testid="activity-calls"] [data-call="source-4"]')).toBeTruthy());
    expect(document.querySelectorAll('[data-testid="activity-calls"] .den-activity-row')).toHaveLength(3);
    await waitFor(() => expect(sink).toHaveBeenCalled());
    const target = sink.mock.calls.at(-1)?.[0];
    expect(target.document.toolCallId).toBe("source-4");
    expect(target.document.pane).toContain("input");
    expect(document.querySelector('[data-testid="activity-input"]')).toBeNull();
  });

  it("opens every step in Files when more exist than the card shows", async () => {
    const sink = filesSink();
    const steps = Array.from({ length: 1000 }, (_, index) => ({ id: String(index), command: `operation ${index} ${"long-input".repeat(100)}`, count: 10705, running: 0, waiting: 0, failed: index === 0 ? 1 : 0, refused: 0, interrupted: 0, state: index === 0 ? "failed" : "completed", outcome: "Finished" }));
    const { view } = await opened(part({ steps }));
    expect(region(view.container, "activity-steps")?.querySelectorAll(".den-activity-row")).toHaveLength(3);
    fireEvent.click(assertNamedControl(view.getByTestId("activity-all-steps"), "button"));
    const content = sink.mock.calls.at(-1)?.[0].document.content;
    expect(content.text).toContain(`operation 999 ${"long-input".repeat(100)}`);
    expect(content.text.split("\n")).toHaveLength(1000);
  });

  it("offers the original arguments while Find reveals them", async () => {
    const { TOOL_CONTENT_REVEAL_EVENT } = await import("../../chat/tool/tool-part-find.ts");
    const sink = filesSink();
    const { view } = await opened(part());
    fireEvent.click(assertNamedControl(view.getByTestId("activity-input"), "button"));
    expect(sink.mock.calls.at(-1)?.[0].document.content).toEqual({ kind: "inline", text: source, redaction: undefined });
    view.container.querySelector('[data-testid="tool-part-card"]')!.dispatchEvent(new CustomEvent(TOOL_CONTENT_REVEAL_EVENT, { detail: { field: "tool_args", from: 4 } }));
    await waitFor(() => {
      fireEvent.click(assertNamedControl(view.getByTestId("activity-input"), "button"));
      expect(sink.mock.calls.at(-1)?.[0].document.pane).toContain(":input:");
    });
  });
});

function formPart(tool: string, args: Record<string, unknown>): ToolPartView {
  const original: ToolPartView = { id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call", tool, kind: "generic", status: "completed", args, output: "form tool output", commandProgram: program };
  return { ...original, commandActivity: { fact: baseFact, members: [baseFact], calls: [original], programIds: [program.id] } };
}

describe("form tool commands", () => {
  const cases: [string, Record<string, unknown>, string][] = [
    ["summarize", { path: "lycaon", task: "Explain the high-level architecture" }, "summarize --path lycaon --focus 'Explain the high-level architecture'"],
    ["survey_repo", { path: "lycaon", bundle: "layout_overview" }, "repo-map --path lycaon --bundle layout_overview"],
    ["code_rewrite", { pattern: "it's here", rewrite: "use bar", path: "src", recursive: true, dry_run: true, lang: "ts" }, "code-rewrite --pattern 'it'\\''s here' --rewrite 'use bar' --path src --recursive --dry-run --lang ts"],
  ];
  for (const [tool, args, command] of cases) {
    it(`synthesizes and copies the ${tool} command`, async () => {
      const sink = filesSink();
      const { view } = await opened(formPart(tool, args));
      fireEvent.click(assertNamedControl(view.getByTestId("activity-target-copy"), "button"));
      await waitFor(() => expect(writeClipboardText).toHaveBeenCalledWith(command));
      fireEvent.click(assertNamedControl(view.getByTestId("activity-input"), "button"));
      expect(sink.mock.calls.at(-1)?.[0].document.content).toEqual({ kind: "inline", text: JSON.stringify(args) });
      expect(view.getByTestId("activity-call-output")).toBeTruthy();
    });
  }
});

it("moves a late commandProgram arrival from the generic card and keeps disclosure state", async () => {
  const { createSignal } = await import("solid-js");
  const { ToolPartCard } = await import("./ToolPartCard.tsx");
  const { createTranscriptDisclosureStore, TranscriptDisclosureProvider } = await import("../../chat/transcript/presentation/disclosure-state.tsx");
  const { transcriptDisclosureKey } = await import("../../chat/transcript/presentation/transcript-disclosure-key.ts");
  const get = vi.fn().mockResolvedValue(page());
  const early: ToolPartView = { id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call", tool: "summarize", kind: "generic", status: "completed", args: { path: "lycaon", task: "Explain" } };
  const [value, setValue] = createSignal(early);
  const store = createTranscriptDisclosureStore();
  const stub = stubClient({ getCommandProgramReceipt: get });
  const view = render(() => <TranscriptDisclosureProvider value={store}><ToolPartCard part={value()} layout="chat" sessionId="activity-card-test" projectId="project" client={stub} /></TranscriptDisclosureProvider>);
  expect(region(view.container, "command-activity-details")).toBeNull();
  fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
  const key = transcriptDisclosureKey.tool("assistant:call");
  expect(store.isOpen(key)).toBe(true);
  setValue({ ...early, commandProgram: program });
  await waitFor(() => expect(region(view.container, "command-activity-details")).toBeTruthy());
  expect(store.isOpen(key)).toBe(true);
  expect(get).toHaveBeenCalledOnce();
});
