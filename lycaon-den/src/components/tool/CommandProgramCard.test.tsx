import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { disclosureToggle } from "../../test/transcript-disclosure-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type {
  CommandExecutionReceipt,
  CommandProgramNode,
  CommandProgramReceipt,
  CommandProgramReceiptPage,
  Message,
} from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { toolPartFromCall } from "../../chat/tool/tool-part-model.ts";
import { ToolPartCard } from "./ToolPartCard.tsx";

const PROGRAM_ID = "6f7d0a8e-1b2c-4d3e-8f90-112233445566";

function program(overrides: Partial<CommandProgramReceipt> = {}): CommandProgramReceipt {
  return {
    id: PROGRAM_ID,
    parent_receipt_id: "0d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6",
    language_version: 1,
    catalog_digest: "catalog",
    source_digest: "source",
    source_ref: "output:exact-program",
    state: "completed",
    status_code: 0,
    detail_ref: "output:program-detail",
    progress_state: "unchanged",
    started_at: "2026-10-20T10:00:00Z",
    settled_at: "2026-10-20T10:00:02Z",
    ...overrides,
  };
}

function execution(id: string, overrides: Partial<CommandExecutionReceipt> = {}): CommandExecutionReceipt {
  return {
    id,
    program_id: PROGRAM_ID,
    node_id: "n1",
    parent_execution_id: "root",
    owner: "files",
    lifecycle: "synchronous",
    definition_digest: "definition",
    arguments_digest: "arguments",
    arguments_ref: `output:arguments-${id}`,
    native_command: "cat",
    effect_classes: ["read_only"],
    command: "cat input.json",
    state: "completed",
    owner_started: true,
    owner_invoked: true,
    status_code: 0,
    complete: true,
    publication_eligible: true,
    output_publishable: true,
    effect_certainty: "none",
    detail_ref: `output:detail-${id}`,
    ...overrides,
  };
}

function node(id: string, line: number, state: CommandProgramNode["state"] = "reached"): CommandProgramNode {
  return { id, source_unit_id: "submitted", start_byte: 0, end_byte: 4, line, column: 1, state };
}

function page(overrides: Partial<CommandProgramReceiptPage> = {}): CommandProgramReceiptPage {
  return {
    program: program(),
    executions: [
      execution("e1"),
      execution("e2", { node_id: "n2", native_command: "jq", command: "jq .name", effect_classes: ["read_only"] }),
      execution("e3", { node_id: "n3", native_command: "write", command: "cat > out.txt <<EOF ...", owner: "files", effect_classes: ["mutation"], effect_certainty: "completed", accepted_revision: "r2", published_revision: "r2" }),
    ],
    nodes: [node("n1", 1), node("n2", 1), node("n3", 2)],
    executions_omitted: 0,
    nodes_omitted: 0,
    ...overrides,
  };
}

/** A real assistant call and tool_result row, through the production view builder. */
function commandPart(receipt: CommandProgramReceipt | undefined, overrides: Partial<ToolPartView> = {}): ToolPartView {
  const call = { id: "call-1", name: "command", args: { command: "cat input.json | jq .name\ncat > out.txt <<EOF\nhello\nEOF" } };
  const result: Message = {
    id: "result-1",
    role: "tool",
    content: "",
    origin: "tool",
    authority: "none",
    trust_tier: "untrusted",
    created_at: "2026-10-20T10:00:02Z",
    tool_result: { content: "out.txt written", tool: "command", tool_call_id: "call-1", command_program: receipt, outcome: "completed" },
  };
  const part = toolPartFromCall(call, result, "assistant-1");
  return { ...part, ...overrides };
}

function renderCard(part: ToolPartView, pages: (cursor?: string) => CommandProgramReceiptPage) {
  const getCommandProgramReceipt = vi.fn(async (_session: string, _program: string, opts?: { cursor?: string }) => pages(opts?.cursor));
  const client = stubClient({ getCommandProgramReceipt });
  const view = render(() => <ToolPartCard part={part} layout="chat" sessionId="session-1" projectId="project-1" client={client} />);
  const card = view.container.querySelector(".den-tool-part[data-transcript-disclosure]");
  fireEvent.click(disclosureToggle(card));
  return { ...view, getCommandProgramReceipt };
}

const section = (container: Element, name: string) => container.querySelector(`[data-testid="command-program-section-${name}"]`);

describe("CommandProgramCard", () => {
  it("renders one card with plain program sections after the existing opening", async () => {
    const { container, getCommandProgramReceipt } = renderCard(commandPart(program()), () => page());
    await waitFor(() => expect(container.querySelectorAll('[data-testid="command-program-stage"]')).toHaveLength(3));
    expect(container.querySelectorAll(".den-tool-part[data-transcript-disclosure]")).toHaveLength(1);
    expect(getCommandProgramReceipt).toHaveBeenCalledWith("session-1", PROGRAM_ID, { limit: 20, cursor: undefined });
    const programText = section(container, "program")?.textContent ?? "";
    expect(programText).toContain("cat > out.txt <<EOF ...");
    expect(programText).not.toContain("hello");
    expect(programText).toContain("Full program");
    expect(section(container, "result")?.textContent).toContain("1 completed, 0 unknown");
    const stages = [...container.querySelectorAll('[data-testid="command-program-stage"]')].map((row) => row.textContent ?? "");
    expect(stages[0]).toContain("Native cat");
    expect(stages[1]).toContain("Line 1, column 1");
    expect(stages[2]).toContain("Completed effect");
    expect(section(container, "changes")?.textContent).toContain("Published revisionr2");
    expect(section(container, "remaining")).toBeNull();
    expect(container.querySelector("[aria-expanded] [data-testid='command-program-stage']")).toBeNull();
    expect(container.querySelector('[data-testid="tool-raw-output-chrome"]')).toBeTruthy();
    expect(container.textContent).not.toMatch(/[A-Z]{4,} [A-Z]{4,}/);
  });

  it("shows failure, partial effects and unknown outcome without a success state", async () => {
    const failed = program({ state: "failed", status_code: 1, stop_code: "HTTP_REQUEST_FAILED" });
    const part = commandPart(failed, {
      status: "error",
      outcome: "rejected",
      feedback: [{ code: "HTTP_REQUEST_FAILED", details: { native_operation: "http_request", operation_source_line: 3, operation_source_column: 1 } }],
    });
    const { container } = renderCard(part, () => page({
      program: failed,
      executions: [
        execution("w1", { native_command: "write", command: "printf ok > a.txt", effect_classes: ["mutation"], effect_certainty: "completed", accepted_revision: "r1", published_revision: "r1" }),
        execution("h1", { node_id: "n2", native_command: "curl", command: "curl -X POST https://example.test", effect_classes: ["network_egress"], effect_certainty: "unknown", state: "failed", status_code: 1, stop_code: "HTTP_REQUEST_FAILED" }),
      ],
      nodes: [node("n1", 1), node("n2", 3), node("n3", 4, "not_started")],
    }));
    await waitFor(() => expect(container.querySelector('[data-testid="command-program-summary"]')?.textContent).toBe("Failed · Partial effects · Unknown outcome"));
    const result = section(container, "result")?.textContent ?? "";
    expect(result).toContain("Failed operationhttp_request");
    expect(result).toContain("SourceLine 3, column 1");
    expect(result).toContain("1 completed, 1 unknown");
    expect(section(container, "changes")?.textContent).toContain("Unknown outcome");
    expect(section(container, "remaining")?.textContent).toContain("Not startedLine 4, column 1");
    expect(container.querySelector('[data-testid="command-program-card"]')?.getAttribute("data-program-state")).toBe("failed");
  });

  it("shows pending approval, held effects and accepted but unpublished changes", async () => {
    const waiting = program({ state: "waiting", status_code: undefined, settled_at: undefined });
    const { container } = renderCard(commandPart(waiting, { status: "running" }), () => page({
      program: waiting,
      executions: [execution("a1", { native_command: "write", command: "printf x > b.txt", state: "waiting", effect_classes: ["mutation"], effect_certainty: "attempted", approval_ref: "checkpoint:approve-1", held_version_id: "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d", accepted_revision: "r4", publication_eligible: false })],
    }));
    await waitFor(() => expect(container.querySelector('[data-testid="command-program-summary"]')?.textContent).toContain("Approval pending"));
    expect(section(container, "stages")?.textContent).toContain("Waiting for approval");
    expect(section(container, "stages")?.textContent).toContain("checkpoint:approve-1");
    const changes = section(container, "changes")?.textContent ?? "";
    expect(changes).toContain("Accepted revisionr4");
    expect(changes).toContain("PublicationAccepted, not published");
    expect(changes).toContain("Held version");
  });

  it("pages repeated loop stages explicitly and keeps the aggregate summary", async () => {
    const many = (from: number) => Array.from({ length: 20 }, (_, i) => execution(`loop-${from + i}`, { command: `cat item-${from + i}`, effect_certainty: from + i === 0 ? "completed" : "none" }));
    const { container, getByTestId, getCommandProgramReceipt } = renderCard(commandPart(program()), (cursor) =>
      cursor === "page-2"
        ? page({ executions: many(20), executions_omitted: 960, nodes: [node("n1", 1)] })
        : page({ executions: many(0), executions_omitted: 980, nodes: [node("n1", 1)], next_cursor: "page-2" }),
    );
    await waitFor(() => expect(container.querySelectorAll('[data-testid="command-program-stage"]')).toHaveLength(20));
    expect(container.textContent).toContain("980 more stages are in retained receipts");
    expect((getByTestId("command-program-previous") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(getByTestId("command-program-next"));
    await waitFor(() => expect(container.querySelector('[data-testid="command-program-stage"]')?.getAttribute("data-execution-id")).toBe("loop-20"));
    expect(getCommandProgramReceipt).toHaveBeenLastCalledWith("session-1", PROGRAM_ID, { limit: 20, cursor: "page-2" });
    expect(container.querySelectorAll('[data-testid="command-program-stage"]')).toHaveLength(20);
    fireEvent.click(getByTestId("command-program-previous"));
    await waitFor(() => expect(container.querySelector('[data-testid="command-program-stage"]')?.getAttribute("data-execution-id")).toBe("loop-0"));
  });

  it("keeps long output in the existing output viewer", async () => {
    const long = "line\n".repeat(5000);
    const { container } = renderCard(commandPart(program(), { output: long }), () => page());
    await waitFor(() => expect(container.querySelectorAll('[data-testid="command-program-stage"]')).toHaveLength(3));
    expect(container.querySelector('[data-testid="tool-raw-output-chrome"]')).toBeTruthy();
    expect(section(container, "program")?.textContent).not.toContain("line\nline");
  });

  it("renders a historical structured command result with the generic card", () => {
    const historical = commandPart(undefined, { args: { command: "go test ./..." }, output: "ok" });
    const getCommandProgramReceipt = vi.fn();
    const { container } = render(() => <ToolPartCard part={historical} layout="chat" sessionId="session-1" client={stubClient({ getCommandProgramReceipt })} />);
    fireEvent.click(disclosureToggle(container.querySelector(".den-tool-part[data-transcript-disclosure]")));
    expect(container.querySelector('[data-testid="command-program-card"]')).toBeNull();
    expect(container.textContent).toContain("go test ./...");
    expect(getCommandProgramReceipt).not.toHaveBeenCalled();
  });

  it("states unavailable receipts without inventing program state", async () => {
    const getCommandProgramReceipt = vi.fn(async () => { throw new Error("offline"); });
    const { container } = render(() => <ToolPartCard part={commandPart(program())} layout="chat" sessionId="session-1" client={stubClient({ getCommandProgramReceipt })} />);
    fireEvent.click(disclosureToggle(container.querySelector(".den-tool-part[data-transcript-disclosure]")));
    await waitFor(() => expect(container.querySelector('[data-testid="command-program-receipt-unavailable"]')).toBeTruthy());
    expect(section(container, "result")?.textContent).toContain("Completed");
    expect(section(container, "stages")).toBeNull();
  });
});
