import { fireEvent, render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import type { ActionDescriptor } from "../../api/types.ts";
import { ACTION_FACT_LABELS, formatActionRow, formatFact } from "../../chat/action/action-format.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
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

  it("names a fold's further targets beside what ran", () => {
    const { container } = render(() => <ActionCard part={part()} action={{ ...descriptor, more: 2 }} layout="chat" />);
    open(container);
    expect(container.querySelector('[data-testid="action-ran"]')?.textContent).toContain("2");
  });
});
