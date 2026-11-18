import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { TOOL_ACTIVITY_PRESENTATION } from "../../tool/tool-presentation.generated.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import type { DisplayTranscriptItem } from "./transcript-item-model.ts";

const CATALOG_TOOLS = Object.keys(TOOL_ACTIVITY_PRESENTATION).sort();
const PATHS = ["a.go", "b.go", "c.go"];

type Rand = (below: number) => number;

function seeded(seed: number): Rand {
  let state = seed >>> 0;
  return (below) => {
    state = (Math.imul(state, 1103515245) + 12345) & 0x7fffffff;
    return state % below;
  };
}

function row(fields: Partial<Message> & Pick<Message, "id" | "role">): Message {
  return {
    origin: "model",
    authority: "none",
    trust_tier: "trusted",
    content: "",
    created_at: "2026-01-01T00:00:00Z",
    workflow_run_id: "run-1",
    visibility: "transcript",
    ...fields,
  } as Message;
}

/**
 * The transcript after each event of a session, in the order the host publishes: a row is
 * appended with the next ordinal, then patched in place.
 */
function sessionStates(seed: number, nextTool: () => string): Message[][] {
  const rand = seeded(seed);
  const byId = new Map<string, Message>();
  const states: Message[][] = [];
  let ord = 0;
  let planSeq = 0;
  const append = (message: Message) => {
    byId.set(message.id, { ...message, ord: ++ord });
    states.push([...byId.values()]);
  };
  const patch = (id: string, fields: Partial<Message>) => {
    byId.set(id, { ...byId.get(id)!, ...fields });
    states.push([...byId.values()]);
  };

  for (let turn = 0; turn < 2; turn++) {
    append(row({ id: `u${turn}`, role: "user", origin: "user", authority: "user", content: "go" }));
    for (let step = 0; step < 3; step++) {
      const carrier = `a${turn}.${step}`;
      const calls = Array.from({ length: 1 + rand(3) }, (_, index) => ({
        id: `${carrier}.c${index}`,
        name: nextTool(),
        args: {},
      }));
      append(row({ id: carrier, role: "assistant", visibility: "internal", draft_status: "live" }));
      patch(carrier, { tool_calls: calls });
      if (rand(2) === 0) append(row({ id: `${carrier}.note`, role: "assistant", kind: "agent_note", content: "Looking." }));
      patch(carrier, { visibility: "transcript", draft_status: "committed" });

      const finishing = [...calls];
      while (finishing.length > 0) {
        const [call] = finishing.splice(rand(finishing.length), 1);
        if (call!.name === "ask_user") {
          append(row({
            id: `${call!.id}.ask`, role: "system", origin: "host", kind: "workflow_feedback",
            workflow_feedback: { phase_id: "", prompt: "Which one?", response_type: "text" },
          }));
        }
        const edits = rand(3) === 0
          ? [fileEditPreviewFixture({ path: PATHS[rand(PATHS.length)]!, before: "old", after: `new ${call!.id}` })]
          : undefined;
        append(row({
          id: `${call!.id}.result`, role: "tool", origin: "tool", trust_tier: "untrusted", content: "ok",
          tool_result: {
            content: "ok", tool: call!.name, tool_call_id: call!.id, assistant_message_id: carrier,
            ui_visibility: rand(4) === 0 ? "benign" : "normal",
            ...(edits ? { file_edit_previews: edits } : {}),
          },
        }));
        if (call!.name === "update_progress") {
          planSeq += 1;
          append(row({
            id: `${call!.id}.plan`, role: "system", origin: "host", kind: "progress_update",
            progress_update: planSeq === 1
              ? { seq: planSeq, initial: true, steps: [{ state: "pending", label: "Plan" }] }
              : { seq: planSeq, initial: false, changes: [{ kind: "done", label: "Plan", state: "done" }] },
          }));
        }
      }
    }
    append(row({ id: `done${turn}`, role: "assistant", content: "Done.", draft_status: "committed" }));
  }
  return states;
}

type Leaf = { key: string; kind: "call" | "diff" | "row"; step: string };

/** Every drawn row, with the assistant step it belongs to. */
function leaves(items: readonly DisplayTranscriptItem[], messages: readonly Message[]): Leaf[] {
  const ordered = [...messages].sort((a, b) => (a.ord ?? 0) - (b.ord ?? 0));
  const stepOf = (id: string) => {
    const at = ordered.findIndex((message) => message.id === id);
    return ordered[at]?.kind === "agent_note" ? ordered[at - 1]!.id : id;
  };
  const out: Leaf[] = [];
  for (const item of items) {
    if (item.kind === "activity_span") {
      for (const entry of item.entries) {
        if (entry.kind === "tool") out.push({ key: entry.part.id, kind: "call", step: entry.part.assistantMessageId });
      }
    } else if (item.kind === "worker_group") {
      for (const part of item.parts) out.push({ key: part.id, kind: "call", step: part.assistantMessageId });
    } else if (item.kind === "tool") {
      out.push({ key: item.part.id, kind: "call", step: item.part.assistantMessageId });
    } else if (item.kind === "file_edit" || item.kind === "worker_file_edit") {
      out.push({ key: item.key, kind: "diff", step: item.key });
    } else {
      out.push({ key: item.key, kind: "row", step: stepOf(item.key) });
    }
  }
  return out;
}

/** Each way an event changed where drawn rows sit. */
function disturbances(states: readonly Message[][], verboseMode: boolean): string[] {
  const project = createTranscriptDisplayProjector();
  const found: string[] = [];
  let drawn: Leaf[] = [];
  states.forEach((messages, event) => {
    const now = leaves(project(messages, undefined, { verboseMode, layout: "chat" })[0]!, messages);
    const position = new Map(now.map((leaf, index) => [leaf.key, index]));
    const before = new Set(drawn.map((leaf) => leaf.key));
    const surviving = drawn.filter((leaf) => position.has(leaf.key));
    for (let i = 1; i < surviving.length; i++) {
      if (position.get(surviving[i - 1]!.key)! > position.get(surviving[i]!.key)!) {
        found.push(`event ${event}: ${surviving[i - 1]!.key} moved below ${surviving[i]!.key}`);
      }
    }
    now.forEach((leaf, index) => {
      if (before.has(leaf.key)) return;
      if (leaf.kind === "diff") {
        if (now[index - 1]?.kind !== "call") found.push(`event ${event}: diff ${leaf.key} is not beneath a call`);
        return;
      }
      const passed = now.slice(index + 1).find(
        (below) => before.has(below.key) && below.kind !== "diff" && below.step !== leaf.step,
      );
      if (passed) found.push(`event ${event}: ${leaf.key} landed above ${passed.key}`);
    });
    drawn = now;
  });
  return found;
}

describe("transcript row stability", () => {
  it.each([false, true])(
    "keeps every drawn row in place as a session arrives (verbose %s)",
    (verboseMode) => {
      let next = 0;
      const called = new Set<string>();
      const nextTool = () => {
        const tool = CATALOG_TOOLS[next++ % CATALOG_TOOLS.length]!;
        called.add(tool);
        return tool;
      };
      for (let seed = 1; seed <= 24; seed++) {
        expect(disturbances(sessionStates(seed, nextTool), verboseMode), `seed ${seed}`).toEqual([]);
      }
      expect([...called].sort()).toEqual(CATALOG_TOOLS);
    },
  );

  it("draws a row's calls once the host commits the row", () => {
    const call = { id: "c1", name: "command", args: { command: "ls" } };
    const drawnCalls = (visibility: "internal" | "transcript") =>
      leaves(
        createTranscriptDisplayProjector()(
          [row({ id: "a1", role: "assistant", ord: 1, visibility, tool_calls: [call] })],
          undefined,
          { layout: "chat" },
        )[0]!,
        [],
      ).filter((leaf) => leaf.kind === "call").length;

    expect(drawnCalls("internal")).toBe(0);
    expect(drawnCalls("transcript")).toBe(1);
  });
});
