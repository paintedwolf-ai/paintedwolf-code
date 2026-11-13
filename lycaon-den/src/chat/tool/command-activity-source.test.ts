import { describe, expect, it } from "vitest";
import type { CommandActivity, CommandActivitySpan, CommandProgramReceipt, HostSecretRedactionMeta } from "../../api/types.ts";
import type { CommandActivityView } from "./command-activity-model.ts";
import { activityTargetCommands, sliceProgramSource } from "./command-activity-source.ts";
import type { ToolPartView } from "./tool-part-model.ts";

const ROOT = "source:submitted";
const encoder = new TextEncoder();

/** Byte span of `needle` in `source`, located on the encoded bytes. */
function span(source: string, needle: string, sourceRef = ROOT): CommandActivitySpan {
  const index = source.indexOf(needle);
  expect(index, `fixture holds ${needle}`).toBeGreaterThanOrEqual(0);
  const start = encoder.encode(source.slice(0, index)).length;
  return { source_ref: sourceRef, start_byte: start, end_byte: start + encoder.encode(needle).length };
}

function fact(id: string, subject: string, spans: CommandActivitySpan[]): CommandActivity {
  return { id, group: "Running programs", phase: "execute", label: "Run process", subject, state: "completed", outcome: "Exited 0", status_code: 0, execution_ids: [id], spans };
}

function program(activities: CommandActivity[]): CommandProgramReceipt {
  return { id: "program", parent_receipt_id: "receipt", language_version: 1, catalog_digest: "catalog", source_digest: "digest", source_ref: ROOT, state: "completed", progress_state: "unchanged", detail_ref: "detail", started_at: "2026-11-13T10:00:00Z", activities };
}

function call(args: Record<string, unknown> | undefined, receipt?: CommandProgramReceipt, extra: Partial<ToolPartView> = {}): ToolPartView {
  return { id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call", tool: "command", kind: "command", status: "completed", args, commandProgram: receipt, ...extra };
}

function view(member: CommandActivity, calls: ToolPartView[], members: CommandActivity[] = [member]): CommandActivityView {
  return { fact: member, calls, members, programIds: ["program"] };
}

/** Each activity of one compound call, as the projection builds it. */
function compound(source: string, facts: CommandActivity[], extra: Partial<ToolPartView> = {}) {
  const receipt = program(facts);
  return facts.map(member => activityTargetCommands(view(member, [call({ command: source }, receipt, extra)])).map(target => target.text));
}

describe("activityTargetCommands", () => {
  it("gives each activity of a compound command its own statement across multi-byte text", () => {
    const source = "mkdir -p mud web && cat > mud/world.py <<'EOF'\n# room — start ✦ 部屋 🐺\nEOF\ngo test ./...";
    const write = "cat > mud/world.py <<'EOF'\n# room — start ✦ 部屋 🐺\nEOF";
    const facts = [
      fact("mkdir", "mud web", [span(source, "mkdir -p mud web")]),
      fact("write", "mud/world.py", [span(source, write)]),
      fact("test", "go test ./...", [span(source, "go test ./...")]),
    ];
    expect(compound(source, facts)).toEqual([["mkdir -p mud web"], [write], ["go test ./..."]]);
  });

  it("joins an activity's separate spans without the text between them", () => {
    const source = "grep -n alpha a.txt; echo between; grep -n beta b.txt";
    const folded = fact("grep", "a.txt", [span(source, "grep -n beta b.txt"), span(source, "grep -n alpha a.txt")]);
    const other = fact("echo", "between", [span(source, "echo between")]);
    expect(compound(source, [folded, other])[0]).toEqual(["grep -n alpha a.txt\ngrep -n beta b.txt"]);
  });

  it("never applies spans from another source unit to the submitted command", () => {
    const source = "eval \"$SCRIPT\"; make test";
    const inner = fact("inner", "rm -rf build", [{ source_ref: "source:eval", start_byte: 0, end_byte: 12 }]);
    const mixed = fact("make", "make test", [{ source_ref: "source:eval", start_byte: 0, end_byte: 4 }, span(source, "make test")]);
    expect(compound(source, [inner, mixed])).toEqual([["rm -rf build"], ["make test"]]);
  });

  it("shows the subject when the submitted text carries a replacement", () => {
    const source = "export TOKEN=[REDACTED]; curl -H \"$TOKEN\" example.test";
    const meta: HostSecretRedactionMeta = { spans: [{ field: "tool_calls.0.args.command", start: 13, length: 10, kind: "secret" }] };
    const facts = [fact("export", "TOKEN", [span(source, "export TOKEN=[REDACTED]")]), fact("curl", "example.test", [span(source, "curl -H \"$TOKEN\" example.test")])];
    expect(compound(source, facts, { argsRedaction: { meta, fieldPrefix: "tool_calls.0.args" } })).toEqual([["TOKEN"], ["example.test"]]);
  });

  it("keeps a single activity's whole command and its redaction field", () => {
    const meta: HostSecretRedactionMeta = { spans: [] };
    const member = fact("only", "cargo test", [{ source_ref: ROOT, start_byte: 0, end_byte: 5 }]);
    const [target] = activityTargetCommands(view(member, [call({ command: "cargo build && cargo test" }, program([member]), { argsRedaction: { meta, fieldPrefix: "tool_result.tool_args" } })]));
    expect(target).toEqual({ text: "cargo build && cargo test", redaction: { meta, field: "tool_result.tool_args.command" } });
  });

  it("shows a form tool's synthesized command", () => {
    const member = fact("map", "repo", []);
    const targets = activityTargetCommands(view(member, [call({ path: "src" }, program([member]), { tool: "survey_repo" })]));
    expect(targets.map(target => target.text)).toEqual(["repo-map --path src"]);
  });

  it("falls back to subjects when arguments were not retained inline", () => {
    const member = fact("spilled", "go test ./...", [{ source_ref: ROOT, start_byte: 0, end_byte: 4 }]);
    expect(activityTargetCommands(view(member, [call(undefined, program([member, fact("other", "x", [])]))])))
      .toEqual([{ text: "go test ./..." }]);
    expect(activityTargetCommands(view(member, [], []))).toEqual([{ text: "go test ./..." }]);
  });

  it("falls back to the subject when no span is in range", () => {
    const source = "ls";
    const facts = [fact("ls", "listing", [{ source_ref: ROOT, start_byte: 1, end_byte: 99 }, { source_ref: ROOT, start_byte: 2, end_byte: 1 }]), fact("pwd", "pwd", [])];
    expect(compound(source, facts)[0]).toEqual(["listing"]);
  });

  it("lists distinct commands once each for a coalesced activity", () => {
    const member = fact("test", "go test", []);
    const calls = ["go test ./a", "go test ./a", "go test ./b"].map(command => call({ command }, program([member])));
    expect(activityTargetCommands(view(member, calls, [member, member, member])).map(target => target.text)).toEqual(["go test ./a", "go test ./b"]);
  });
});

describe("sliceProgramSource", () => {
  it("merges overlapping spans and trims surrounding blank lines", () => {
    const source = "\n\nalpha beta gamma\n\n";
    const spans = [{ source_ref: ROOT, start_byte: 0, end_byte: 9 }, { source_ref: ROOT, start_byte: 4, end_byte: 18 }];
    expect(sliceProgramSource(source, spans, ROOT)).toBe("alpha beta gamma");
  });
});
