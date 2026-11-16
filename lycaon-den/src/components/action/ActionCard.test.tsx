import { createSignal } from "solid-js";
import { fireEvent, render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import type { ActionDescriptor } from "../../api/types.ts";
import { ACTION_FACT_LABELS, formatActionRow, formatFact } from "../../chat/action/action-format.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { CardAction } from "../../chat/action/action-merge.ts";
import { NetworkActionContext } from "../../chat/network-action-context.ts";
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
    const network: CardAction = { ...descriptor, facts: [{ kind: "network", hosts: ["a.test", "b.test"], egress: "proxy_only" }], row: ["network"] };
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
});
