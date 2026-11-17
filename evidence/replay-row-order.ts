import { readFileSync } from "node:fs";
const SRC = process.env.SRC!;
const { createTranscriptDisplayProjector } = await import(`${SRC}/chat/transcript/projection/transcript-display-projection.ts`);

type Leaf = { key: string; kind: string; step: string };
function leaves(items: any[], messages: any[]): Leaf[] {
  const ordered = [...messages].sort((a, b) => a.ord - b.ord);
  const stepOf = (id: string) => { const at = ordered.findIndex((m) => m.id === id); return ordered[at]?.kind === "agent_note" ? ordered[at - 1]?.id ?? id : id; };
  const out: Leaf[] = [];
  for (const item of items) {
    if (item.kind === "activity_span") { for (const e of item.entries) if (e.kind === "tool") out.push({ key: e.part.id, kind: "command", step: e.part.assistantMessageId }); }
    else if (item.kind === "worker_group") for (const p of item.parts) out.push({ key: p.id, kind: "command", step: p.assistantMessageId });
    else if (item.kind === "tool") out.push({ key: item.part.id, kind: "command", step: item.part.assistantMessageId });
    else if (item.kind === "file_edit" || item.kind === "worker_file_edit") out.push({ key: item.key, kind: "diff", step: item.key });
    else if (item.kind === "checkpoint") out.push({ key: item.key, kind: "approval", step: item.parentMessageId });
    else out.push({ key: item.key, kind: item.kind === "progress_update" ? "plan" : ordered.find((m) => m.id === item.key)?.kind === "agent_note" ? "note" : item.kind, step: stepOf(item.key) });
  }
  return out;
}
function notesFirst(events: any[]): any[] {
  const out = events.filter((e) => e.message);
  for (const note of out.filter((e) => e.message.kind === "agent_note" && e.op === "append")) {
    const at = out.findIndex((e) => e.message.ord === note.message.ord - 1 && e.message.role === "assistant" && e.message.tool_calls?.length && e.message.visibility === "transcript");
    const from = out.indexOf(note);
    if (at >= 0 && at < from) { out.splice(from, 1); out.splice(at, 0, note); }
  }
  return out;
}
const bump = (o: Record<string, number>, k: string) => { o[k] = (o[k] ?? 0) + 1; };
const sessions = JSON.parse(readFileSync(process.argv[2]!, "utf8"));
for (const host of ["as captured", "note stored first"]) {
  const live: Record<string, number> = {}; let steps = 0, disturbed = 0, groups = 0;
  for (const [name, events] of Object.entries<any[]>(sessions)) {
    
    const project = createTranscriptDisplayProjector();
    const byId = new Map<string, any>(); let prev: Leaf[] = []; let last: any[] = [];
    for (const ev of host === "as captured" ? events.filter((e) => e.message) : notesFirst(events)) {
      byId.set(ev.message.id, ev.message);
      const messages = [...byId.values()];
      last = project(messages, undefined, { layout: "chat", verboseMode: false })[0];
      const cur = leaves(last, messages); steps++;
      const pos = new Map(cur.map((l, i) => [l.key, i])); const before = new Set(prev.map((l) => l.key));
      const surviving = prev.filter((l) => pos.has(l.key)); let hit = false;
      for (let i = 1; i < surviving.length; i++) if (pos.get(surviving[i - 1]!.key)! > pos.get(surviving[i]!.key)!) { bump(live, `drawn ${surviving[i - 1]!.kind} moved below ${surviving[i]!.kind}`); hit = true; }
      cur.forEach((l, i) => {
        if (before.has(l.key)) return;
        if (l.kind === "diff") { if (cur[i - 1]?.kind !== "command") { bump(live, "diff not beneath a command"); hit = true; } return; }
        const passed = cur.slice(i + 1).find((x) => before.has(x.key) && x.kind !== "diff" && x.step !== l.step);
        const same = cur.slice(i + 1).find((x) => before.has(x.key) && x.kind !== "diff");
        if (passed) { bump(live, `new ${l.kind} above another step's ${passed.kind}`); hit = true; }
        else if (same) { bump(live, `new ${l.kind} above its own step's ${same.kind}`); hit = true; }
      });
      if (hit) { disturbed++; if (host === "as captured") { const m = ev.message; console.log(`[${name.slice(-8)} ${ev.op} ord=${m.ord} ${m.role} ${m.kind ?? ""} vis=${m.visibility} calls=${(m.tool_calls ?? []).map((c: any) => c.name)} result=${m.tool_result?.tool ?? ""} of=${m.tool_result ? byId.get(m.tool_result.assistant_message_id)?.ord : ""}]\n  before: ${prev.slice(-8).map((l) => `${byId.get(l.step)?.ord ?? "?"}:${l.kind}`).join(" ")}\n  after:  ${cur.slice(-9).map((l) => `${byId.get(l.step)?.ord ?? "?"}:${l.kind}`).join(" ")}`); } }
      prev = cur;
    }
    groups += last.filter((i: any) => i.kind === "activity_span").length;
  }
  console.log(JSON.stringify({ host, steps, disturbed, groups, live }));
}
