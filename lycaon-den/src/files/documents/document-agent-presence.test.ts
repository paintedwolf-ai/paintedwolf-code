// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import * as Y from "yjs";
import { createSignal } from "solid-js";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import type { AgentSessionPresence } from "../../api/types.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { getOverviewAgentMarks } from "../../components/source/annotations/overview-agent-marks.ts";
import type { AgentMark } from "../components/agent-presence.ts";
import {
  AGENT_FADE_OUT_MS,
  agentWholeFile,
  agentPaintField,
  documentAgentPresence,
  nextAgentPaint,
  resolveAgentMark,
  type PaintedAgentMark,
} from "./document-agent-presence.ts";
import { encodeUpdate } from "./document-outbox.ts";
import { DocumentReplica } from "./document-replica.ts";

vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "local" }));
const cleanup: (() => void | Promise<void>)[] = [];
afterEach(async () => {
  for (const stop of cleanup.splice(0).reverse()) await stop();
  resetEditorPrefsForTests();
});

const TEXT = "first line\nsecond line\nthird line";

async function fixture(initial: AgentSessionPresence[] = [], open?: string) {
  const host = new DocumentFixture(TEXT);
  const client = stubClient({ syncEditorDocument: host.sync, publishEditorDocumentPresence: async () => undefined, leaveEditorDocument: async () => undefined });
  const replica = new DocumentReplica(host.snapshot(), () => client, () => undefined, memoryDocumentOutbox());
  await replica.initialize();
  cleanup.push(() => replica.close());
  const [sessions, setSessions] = createSignal<ReadonlyMap<string, AgentSessionPresence>>(
    new Map(initial.map(presence => [presence.session_id, presence])));
  const parent = document.createElement("div");
  document.body.append(parent);
  const view = new EditorView({ parent, state: EditorState.create({ doc: replica.text.toString(),
    extensions: documentAgentPresence(replica, { sessions, openSessionId: () => open }) }) });
  cleanup.push(() => { view.destroy(); parent.remove(); });
  const position = (index: number) => encodeUpdate(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(replica.text, index)));
  const publish = (...chats: AgentSessionPresence[]) => setSessions(new Map(chats.map(presence => [presence.session_id, presence])));
  return { replica, view, position, publish };
}

function chat(fields: Partial<AgentSessionPresence>): AgentSessionPresence {
  return { session_id: "s1", title: "Fix login", turn: 1, activities: [], reads: [], intents: [], worker_drafts: [], ...fields };
}

const readItem = (fields: Partial<AgentSessionPresence["reads"][number]>) => ({
  id: "read-1", sequence: 1, tool_call_id: "call", tool: "read", root_id: "root-1", path: "a.txt",
  extent: "range" as const, ranges: [{ start_line: 2, end_line: 2 }], stale: false, ...fields,
});

const painted = (view: EditorView) => view.state.field(agentPaintField).marks;

describe("agent presence in a document", () => {
  it("follows anchors through edits and falls back to lines for another document", async () => {
    const { replica, view, position } = await fixture();
    const base = { source: { kind: "read" as const, item: readItem({}) }, key: "k", chat: { sessionId: "s1", title: "Fix login", slot: 0 }, kind: "read" as const, stale: false, caret: false };
    const anchored: AgentMark = { ...base, documentId: "document-1", epoch: 1,
      range: { start_line: 2, end_line: 2, anchor: position(11), head: position(22) } };
    replica.text.insert(0, "new\n");
    expect(resolveAgentMark(replica, EditorState.create({ doc: replica.text.toString() }).doc, anchored)).toEqual({ from: 15, to: 26 });
    const otherDocument: AgentMark = { ...anchored, documentId: "document-2" };
    expect(resolveAgentMark(replica, view.state.doc, otherDocument)).toEqual({ from: 11, to: 22 });
    const pastEnd: AgentMark = { ...base, kind: "insertion", range: { start_line: 9, end_line: 9, start_character: 0, end_character: 0 } };
    expect(resolveAgentMark(replica, view.state.doc, pastEnd)).toEqual({ from: TEXT.length, to: TEXT.length });
  });

  it("fades departed marks out before dropping them", () => {
    const chatRef = { sessionId: "s1", title: "Fix login", slot: 0 };
    const mark: AgentMark = { source: { kind: "read", item: readItem({}) }, key: "k", chat: chatRef, kind: "read", stale: false, caret: false, range: { start_line: 1, end_line: 1 } };
    const shown = nextAgentPaint([], [{ mark, from: 0, to: 5, color: undefined }], 1_000);
    expect(shown).toEqual([expect.objectContaining({ bornAt: 1_000, leftAt: null })]);
    expect(nextAgentPaint(shown, [{ mark, from: 0, to: 5, color: undefined }], 1_500)[0]?.bornAt).toBe(1_000);
    const leaving = nextAgentPaint(shown, [], 2_000);
    expect(leaving).toEqual([expect.objectContaining({ leftAt: 2_000 })]);
    expect(nextAgentPaint(leaving, [], 2_000 + AGENT_FADE_OUT_MS - 1)).toHaveLength(1);
    expect(nextAgentPaint(leaving, [], 2_000 + AGENT_FADE_OUT_MS)).toHaveLength(0);
  });

  it("keeps pending whole-file changes out of the read tint", () => {
    const chatRef = { sessionId: "s1", title: "Fix login", slot: 0 };
    const item = (key: string, kind: AgentMark["kind"], bornAt: number, leftAt: number | null = null): PaintedAgentMark => ({
      mark: { source: { kind: "read", item: readItem({ sequence: bornAt }) }, key, chat: chatRef, kind, stale: false, caret: false }, from: 0, to: 0, color: undefined, bornAt, leftAt,
    });
    expect(agentWholeFile([item("old", "whole", 1), item("new", "whole", 2)])?.mark.key).toBe("new");
    expect(agentWholeFile([item("read", "whole", 3), item("pending", "whole-pending", 1)])?.mark.key).toBe("read");
    expect(agentWholeFile([item("gone", "whole-pending", 1, 5), item("read", "whole", 2)])?.mark.key).toBe("read");
  });

  it("paints matches, the whole-file gutter tint, labeled changes, and ruler ticks, then fades them when the turn ends", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    cleanup.push(() => { vi.useRealTimers(); });
    const { view, publish } = await fixture([chat({
      reads: [
        readItem({ id: "whole", extent: "whole_file", ranges: [] }),
        readItem({ id: "match", sequence: 2, extent: "matches", ranges: [{ start_line: 2, end_line: 2, start_character: 0, end_character: 6 }] }),
      ],
      intents: [{
        id: "intent", tool_call_id: "c2", tool: "edit", operation: "edit", state: "awaiting_approval", root_id: "root-1",
        path: "a.txt", extent: "range", ranges: [{ start_line: 3, end_line: 3 }],
      }],
    })]);
    await vi.waitFor(() => expect(painted(view)).toHaveLength(3));
    expect(view.dom.querySelector(".cm-agent-match")).toBeNull();
    expect(view.dom.classList.contains("cm-agent-file")).toBe(true);
    // This fixture has no layout geometry for label positioning.
    expect(painted(view).find(item => item.mark.label)?.mark.label).toBe("waiting for your approval to edit");
    // Only ranged marks have positions on the ruler.
    expect(getOverviewAgentMarks(view.state).map(mark => mark.kind).sort()).toEqual(["agent", "agent-pending"]);

    publish(chat({}));
    await vi.waitFor(() => expect(painted(view).every(mark => mark.leftAt !== null)).toBe(true));
    expect(view.dom.classList.contains("cm-agent-file--leaving")).toBe(true);
    await vi.advanceTimersByTimeAsync(AGENT_FADE_OUT_MS + 50);
    await vi.waitFor(() => expect(painted(view)).toHaveLength(0));
    expect(view.dom.classList.contains("cm-agent-file")).toBe(false);
    expect(getOverviewAgentMarks(view.state)).toEqual([]);
  });

  it("shows nothing when agent activity is off in settings", async () => {
    resetEditorPrefsForTests({ agentActivity: false });
    const { view } = await fixture([chat({ reads: [readItem({})] })]);
    await Promise.resolve();
    await Promise.resolve();
    expect(painted(view)).toEqual([]);
  });
});
