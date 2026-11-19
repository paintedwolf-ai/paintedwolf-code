import { createSignal } from "solid-js";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { ActionDescriptor } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { ACTION_FACT_LABELS, formatActionRow, formatFact } from "../../chat/action/action-format.ts";
import { pendingAction } from "../../chat/action/part-action.ts";
import { retiredToolNames } from "../../chat/tool/tool-catalog-tools.ts";
import { searchToolPart, toolPartFindCorpus } from "../../chat/tool/tool-part-find.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { CardAction } from "../../chat/action/action-merge.ts";
import { NetworkActionContext } from "../../chat/network-action-context.ts";
import { invalidateSourceQueries } from "../../files/source/source-invalidation.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { ActionCard } from "./ActionCard.tsx";

function part(): ToolPartView {
  return { id: "row", toolCallId: "call", assistantMessageId: "a", messageId: "m", tool: "terminal_close", kind: "generic", status: "completed", args: { id: "t" }, output: "{}" };
}

const descriptor: ActionDescriptor = {
  label: "Terminal session",
  status: "done",
  ran: { text: "python3 game.py", noun: "command" },
  facts: [
    { kind: "result", outcome: "completed" },
    { kind: "took", duration_ms: 38_000 },
    { kind: "steps", count: 4, unit: "actions", sequence: ["open", "send", "send", "close"] },
    { kind: "stopped_by", by: "host_stop", code: "TERMINAL_IDLE" },
  ],
  row: ["result", "steps"],
  preview: { kind: "output", lines: [{ text: " X | O | X " }, { text: "It's a draw!" }] },
  links: [{ kind: "input" }, { kind: "output" }],
  opener: { kind: "running" },
};

function open(container: HTMLElement) {
  fireEvent.click(container.querySelector('[data-testid="action-card"] > button')!);
}

describe("ActionCard", () => {
  it("states the descriptor's label and row outcome in the row", () => {
    const { container } = render(() => <ActionCard part={part()} action={descriptor} layout="chat" />);
    const row = container.querySelector('[data-testid="action-card"] > button')!;
    expect(row.textContent).toContain(descriptor.label);
    expect(row.textContent).toContain(formatActionRow(descriptor));
    expect(container.querySelector('[data-testid="action-card"]')?.getAttribute("data-status")).toBe("done");
  });

  it("shows incomplete file observation on the row and explains the covered folder in the card", () => {
    const action: ActionDescriptor = {
      ...descriptor,
      facts: [{ kind: "source_observation", source_coverage: [{ folder: "/srv/shared", coverage: "not_observed", reason: "time_budget" }] }],
      row: ["source_observation"],
    };
    const { container } = render(() => <ActionCard part={part()} action={action} layout="chat" />);
    const row = container.querySelector('[data-testid="action-card"] > button')!;
    expect(row.textContent).toContain("File observation incomplete");
    open(container);
    const coverage = container.querySelector('[data-kind="source_observation"] dd');
    expect(coverage?.textContent).toContain("/srv/shared was not observed: the observation ran out of time.");
  });

  it("refreshes a pending command window when its observation settles", async () => {
    const get = vi.fn()
      .mockResolvedValueOnce({ settling: true, coverage: [] })
      .mockResolvedValueOnce({
        settling: false,
        coverage: [{ folder: "/srv/shared", coverage: "not_observed", reason: "io_error" }],
      });
    const client = stubClient({ getProjectSourceCommandObservation: get });
    const action: ActionDescriptor = {
      ...descriptor,
      facts: [{ kind: "source_observation", source_windows: ["window"] }],
      row: [],
      opener: undefined,
    };
    const { container } = render(() => <ActionCard part={part()} action={action} layout="chat" sessionId="chat" projectId="project" client={client} />);
    const row = container.querySelector('[data-testid="action-card"] > button')!;
    expect(row.textContent).not.toContain("File observation incomplete");
    expect(get).toHaveBeenCalledWith("project", "chat", "window", expect.any(AbortSignal));
    invalidateSourceQueries({ projectId: "other" });
    expect(get).toHaveBeenCalledTimes(1);
    invalidateSourceQueries({ projectId: "project" });
    await waitFor(() => expect(row.textContent).toContain("File observation incomplete"));
    invalidateSourceQueries({ projectId: "project" });
    expect(get).toHaveBeenCalledTimes(2);
    open(container);
    expect(container.querySelector('[data-kind="source_observation"] dd')?.textContent).toContain("/srv/shared was not observed");
  });

  it("stops refreshing when the host reports a pruned window", async () => {
    const get = vi.fn().mockRejectedValue(new LycaonApiError("Window pruned", undefined, "not_found"));
    const client = stubClient({ getProjectSourceCommandObservation: get });
    const action: ActionDescriptor = {
      ...descriptor,
      facts: [{ kind: "source_observation", source_windows: ["window"] }],
      row: [],
      opener: undefined,
    };
    const { container } = render(() => <ActionCard part={part()} action={action} layout="chat" sessionId="chat" projectId="project" client={client} />);
    await get.mock.results[0]?.value.catch(() => {});
    invalidateSourceQueries({ projectId: "project" });
    expect(get).toHaveBeenCalledTimes(1);
    expect(container.textContent).not.toContain("File observation incomplete");
  });

  it("opens to what ran, the summary, facts, preview and records, in that order and nothing else", () => {
    const summary = <div data-testid="supplied-summary">supplied summary</div>;
    const evidence = <button type="button" data-testid="supplied-evidence">supplied evidence</button>;
    const { container } = render(() => <ActionCard part={part()} action={descriptor} layout="chat" slots={{ summary, evidence }} />);
    open(container);
    const body = container.querySelector('[data-testid="action-card-body"]')!;
    const regions = [...body.children].map((child) => child.getAttribute("data-testid"));
    expect(regions).toEqual(["action-ran", "supplied-summary", "action-facts", "action-preview", "action-links"]);
    const facts = [...body.querySelectorAll('[data-testid="action-facts"] > div')];
    expect(facts.map((row) => row.querySelector("dt")?.textContent)).toEqual(descriptor.facts.map((fact) => ACTION_FACT_LABELS[fact.kind]));
    expect(facts.map((row) => row.querySelector("dd")?.textContent)).toEqual(descriptor.facts.map(formatFact));
    expect(facts.at(-1)?.getAttribute("data-tone")).toBe("danger");
    const links = [...body.querySelectorAll('[data-testid="action-links"] > *')].map((node) => node.getAttribute("data-testid") ?? node.querySelector("[data-testid]")?.getAttribute("data-testid"));
    expect(links).toEqual(["action-link-input", "action-link-output", "supplied-evidence", "action-opener"]);
  });

  it("shows nothing in its body that the descriptor and slots did not supply", () => {
    const { container } = render(() => <ActionCard part={part()} action={descriptor} layout="chat" />);
    open(container);
    const body = container.querySelector('[data-testid="action-card-body"]')!;
    const allowed = [
      descriptor.ran.text,
      ...descriptor.facts.flatMap((fact) => [ACTION_FACT_LABELS[fact.kind], formatFact(fact)]),
      ...(descriptor.preview?.lines ?? []).map((line) => line.text),
    ];
    const visible = [...body.querySelectorAll("code, dt, dd, [data-testid='action-preview'] span")].map((node) => node.textContent ?? "");
    for (const text of visible) expect(allowed).toContain(text);
  });

  it("offers to block each allowed host on the network fact, and nothing more", () => {
    const reached = (host: string, decision: "allow" | "deny"): ToolPartView => ({
      ...part(), id: host, toolCallId: host,
      externalAccess: { modes: ["mediated_http"], visibility_summary: "observed",
        endpoints: [{ host, port: 443, transport: "http_connect", visibility: "observed", decision, attempt_count: 1 }] },
      redaction: { spans: [{ field: "tool_result.content", start: 0, length: 6, kind: "secret" }] },
    });
    const blocked = new Set<string>();
    const [, setTick] = createSignal(0);
    const actions = { isBlocked: (host: string) => blocked.has(host), blockHost: async (host: string) => { blocked.add(host); setTick((n) => n + 1); } };
    const network: CardAction = { ...descriptor, facts: [{ kind: "network", hosts: ["a.test", "b.test"], egress: "proxy_only" }], row: ["network"], preview: undefined };
    const { container } = render(() => (
      <NetworkActionContext.Provider value={actions}>
        <ActionCard part={{ ...reached("a.test", "allow"), foldMembers: [reached("b.test", "deny")] }} action={network} layout="chat" />
      </NetworkActionContext.Provider>
    ));
    open(container);
    const body = container.querySelector('[data-testid="action-card-body"]')!;
    const buttons = [...body.querySelectorAll('[data-testid="action-block-host"]')];
    expect(buttons.map((button) => button.textContent)).toEqual(["Block a.test"]);
    expect(buttons[0]!.closest("[data-kind]")?.getAttribute("data-kind")).toBe("network");
    expect([...body.children].map((child) => child.getAttribute("data-testid"))).toEqual(["action-ran", "action-facts", "action-links"]);
  });

  it("keeps unobserved external access visible with no endpoints, distinct for each egress", () => {
    const unobserved: ToolPartView = {
      ...part(), tool: "command", kind: "command",
      externalAccess: { modes: ["direct_ip"], visibility_summary: "unobserved", direct: { scope: "current_action", actual_destination_visibility: "unobserved" } },
    };
    const shown = (["direct", "local_only", "proxy_only"] as const).map((egress) => {
      const action: CardAction = { ...descriptor, facts: [{ kind: "network", egress }], row: ["network"], preview: undefined };
      const { container, unmount } = render(() => <ActionCard part={unobserved} action={action} layout="chat" />);
      open(container);
      const value = container.querySelector('[data-testid="action-facts"] > [data-kind="network"] dd')?.textContent?.trim() ?? "";
      unmount();
      return value;
    });
    for (const value of shown) expect(value).not.toBe("");
    expect(new Set(shown).size).toBe(shown.length);
  });

  it("shows a live process's own state and Stop in place of its recorded process fact", () => {
    const running: CardAction = { ...descriptor, facts: [{ kind: "process", state: "running", port: 5173 }], row: ["process"] };
    const live: ToolPartView = { ...part(), process: { handle: "job-1", running: true } };
    const { container } = render(() => <ActionCard part={live} action={running} layout="chat" sessionId="s" />);
    open(container);
    const body = container.querySelector('[data-testid="action-card-body"]')!;
    expect(body.querySelector('[data-testid="action-facts"]')).toBeNull();
    expect(body.querySelector('[data-testid="background-process-running"]')?.textContent).toBe("Running · listening on 5173");
    expect(body.querySelector('[data-testid="background-process-stop"]')).not.toBeNull();
    const recorded = render(() => <ActionCard part={live} action={running} layout="chat" />);
    open(recorded.container);
    expect(recorded.container.querySelector('[data-testid="action-process"]')).toBeNull();
    expect(recorded.container.querySelector('[data-testid="action-facts"]')?.textContent).toContain("5173");
  });

  it("draws only a fold's newest capture", () => {
    const capture = (id: string): ToolPartView => ({ ...part(), id, toolCallId: id, tool: "page_snapshot",
      visual: { id, mime: "image/png", source: "capture", caption: `Capture ${id}`, bytes: "iVBORw0KGgo=" } });
    const { container } = render(() => <ActionCard part={{ ...capture("one"), foldMembers: [capture("two"), part()] }} action={descriptor} layout="chat" sessionId="s" />);
    open(container);
    const shown = [...container.querySelectorAll('[data-testid="action-card-body"] [data-testid="transcript-visual-artifact"]')];
    expect(shown.map((node) => node.getAttribute("data-artifact-id"))).toEqual(["two"]);
  });

  it("opens a file destination through the project's paths", () => {
    const { container } = render(() => <ActionCard part={part()} action={{ ...descriptor, opener: { kind: "file", path: "@other/src" } }} layout="chat" projectId="p" />);
    open(container);
    const opener = container.querySelector('[data-testid="action-opener"]')!;
    expect(opener.querySelector('[data-testid="source-path-link"]')?.textContent).toBe("Open in Files");
  });

  it("keeps its record links mounted when the same descriptor arrives as a new object", () => {
    const [current, setCurrent] = createSignal<CardAction>(descriptor);
    const { container } = render(() => <ActionCard part={part()} action={current()} layout="chat" projectId="p" />);
    open(container);
    const output = container.querySelector('[data-testid="action-link-output"]');
    const opener = container.querySelector('[data-testid="action-opener"]');
    setCurrent(structuredClone(descriptor));
    expect(container.querySelector('[data-testid="action-link-output"]')).toBe(output);
    expect(container.querySelector('[data-testid="action-opener"]')).toBe(opener);
  });

  it("ends its links with the opener and attaches the output to chat when dragged", () => {
    const { container } = render(() => <ActionCard part={part()} action={descriptor} layout="chat" projectId="p" sessionId="s" />);
    open(container);
    const links = [...container.querySelectorAll('[data-testid="action-links"] [data-testid]')].map((node) => node.getAttribute("data-testid"));
    expect(links).toEqual(["action-link-input", "action-link-output", "action-opener"]);
    expect(container.querySelector('[data-testid="action-links"]')?.getAttribute("draggable")).toBe("true");
  });

  it("names a fold's further targets beside what ran", () => {
    const { container } = render(() => <ActionCard part={part()} action={{ ...descriptor, more: 2 }} layout="chat" />);
    open(container);
    expect(container.querySelector('[data-testid="action-ran"]')?.textContent).toContain("2");
  });

  it("formats every action fact kind and compact row outcome", () => {
    expect(formatFact({ kind: "took", duration_ms: 250 })).toBe("250 ms");
    expect(formatFact({ kind: "took", duration_ms: 125_000 })).toBe("2m 5s");
    expect(formatFact({ kind: "result", outcome: "failed", exit_code: 2 })).toBe("Failed · exit 2");
    expect(formatFact({ kind: "result", exit_code: 1 })).toBe("Exit 1");
    expect(formatFact({ kind: "result", outcome: "passed", http_status: 200 })).toBe("Passed · 200");
    expect(formatFact({ kind: "result", outcome: "committed", ref: "abc1234" })).toBe("Committed abc1234");
    expect(formatFact({ kind: "changed", created: 1 })).toBe("New file");
    expect(formatFact({ kind: "changed", created: 2, files: 3, added: 10, removed: 4, hunks: 2 })).toBe("2 files new · 3 files · +10 −4 in 2 hunks");
    expect(formatFact({ kind: "found", items: ["read_file", "grep"] })).toBe("read_file · grep");
    expect(formatFact({ kind: "found", high: 1, low: 2 })).toBe("1 high · 2 low");
    expect(formatFact({ kind: "found", count: 3, noun: "matches", files: 2 })).toBe("3 matches in 2 files");
    expect(formatFact({ kind: "steps", count: 3, unit: "calls", failed: 1 })).toBe("3 calls · 1 failed");
    expect(formatFact({ kind: "stopped_by", by: "approval", decided_by: "policy", decided_at: "2026-11-16T10:00:00Z" })).toContain("Approved by policy · ");
    expect(formatFact({ kind: "stopped_by", by: "budget" })).toBe("Budget limit");
    expect(formatFact({ kind: "stopped_by", by: "policy", code: "DENY_NET" })).toBe("Policy · DENY_NET");
    expect(formatFact({ kind: "worker", role: "implementer", state: "working" })).toBe("implementer · working");
    expect(formatFact({ kind: "network", egress: "direct" })).toBe("direct");
    expect(formatFact({ kind: "network", hosts: ["a.test"], egress: "direct" })).toBe("a.test · direct");
    expect(formatFact({ kind: "network", egress: "local_only" })).toBe("local only");
    expect(formatFact({ kind: "budget", used: 25, limit: 100 })).toBe("25% of 100");
    expect(formatFact({ kind: "budget", used: 5 })).toBe("5");
    expect(formatFact({ kind: "captured", caption: "Dashboard" })).toBe("Dashboard");
    expect(formatFact({ kind: "containment", mode: "sandbox" })).toBe("sandbox");
    expect(formatFact({ kind: "loaded_by", by: "engine" })).toBe("Local AI");

    expect(formatActionRow({
      ...descriptor,
      facts: [
        { kind: "result", outcome: "failed", exit_code: 1 },
        { kind: "changed", added: 8, removed: 3 },
        { kind: "steps", count: 2, unit: "calls" },
      ],
      row: ["result", "changed", "steps"],
    })).toBe("Exited 1 · +8 −3 · 2 steps");
    expect(formatActionRow({ ...descriptor, status: "running", facts: [], row: [] })).toBe("Running");
  });

  it("renders diff preview locations, signs, and omitted line counts", () => {
    const diffAction: CardAction = {
      ...descriptor,
      preview: {
        kind: "diff",
        lines: [
          { location: "src/app.ts:12", sign: "added", text: "const x = 1;" },
          { sign: "removed", text: "const x = 0;" },
        ],
        lines_omitted: 3,
      },
    };
    const { container } = render(() => <ActionCard part={part()} action={diffAction} layout="chat" />);
    open(container);
    const preview = container.querySelector('[data-testid="action-preview"]');
    expect(preview?.getAttribute("data-kind")).toBe("diff");
    expect(preview?.textContent).toContain("src/app.ts:12");
    expect(preview?.textContent).toContain("+ const x = 1;");
    expect(preview?.textContent).toContain("− const x = 0;");
    expect(preview?.textContent).toContain("3 more");
  });

  it("loads execution receipt output when an output preview names receipt scopes", async () => {
    const getCommandProgramReceipt = vi.fn().mockResolvedValue({
      executions: [
        {
          execution_id: "exec-1",
          state: "completed",
          status_code: 0,
          outputs: [
            {
              stream: "stdout",
              ref: "ref-1",
              size_bytes: 14,
              complete: false,
              redacted: false,
              preview: "line one\nline two\n",
              preview_truncated: true,
              line_count: 10,
            },
          ],
        },
      ],
    });
    const client = stubClient({ getCommandProgramReceipt });
    const receiptAction: CardAction = {
      ...descriptor,
      preview: { kind: "output", receipts: [{ program_id: "prog-1", activity_id: "act-1" }] },
    };
    const { container } = render(() => (
      <ActionCard part={part()} action={receiptAction} layout="chat" sessionId="sess-1" client={client} />
    ));
    open(container);
    await waitFor(() => {
      const text = container.querySelector('[data-testid="action-preview"]')?.textContent ?? "";
      expect(text).toContain("line one");
      expect(text).toContain("First 2 of 10 lines");
    });
    expect(getCommandProgramReceipt).toHaveBeenCalledWith("sess-1", "prog-1", {
      limit: 6,
      cursor: undefined,
      activity_id: "act-1",
    });
  });

  it("indexes and searches folded tool part members", async () => {
    const folded: ToolPartView = {
      ...part(),
      toolCallId: "lead-call",
      output: "alpha",
      foldMembers: [
        { ...part(), id: "row-2", toolCallId: "member-call", output: "found needle here" },
      ],
    };
    expect(toolPartFindCorpus(folded)).toContain("found needle here");
    const matches = await searchToolPart(folded, "sess-1", "needle", false, new AbortController().signal);
    expect(matches).toEqual([
      { from: 6, to: 12, field: "tool_output", toolCallId: "member-call" },
    ]);
  });

  it("renders every retired tool a stored transcript may name", () => {
    const retired = retiredToolNames();
    expect(retired.length).toBeGreaterThan(0);
    for (const tool of retired) {
      const p = { ...part(), tool, output: JSON.stringify({ status: "completed" }) };
      const { container, unmount } = render(() => (
        <ActionCard part={p} action={pendingAction(p)} layout="chat" />
      ));
      expect(container.querySelector('[data-testid="action-card"]'), tool).not.toBeNull();
      unmount();
    }
  });
});
