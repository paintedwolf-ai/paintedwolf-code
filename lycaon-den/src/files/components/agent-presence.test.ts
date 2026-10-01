import { describe, expect, it } from "vitest";
import type { AgentSessionPresence } from "../../api/types.ts";
import {
  agentChats,
  agentDocumentMarks,
  agentFileKey,
  projectAgentFiles,
} from "./agent-presence.ts";

const ALL = { reads: true, changes: true, fileNames: true };

function chat(sessionId: string, fields: Partial<AgentSessionPresence> = {}): AgentSessionPresence {
  return { session_id: sessionId, turn: 1, activities: [], reads: [], intents: [], worker_drafts: [], ...fields };
}

function sessions(...chats: AgentSessionPresence[]): Map<string, AgentSessionPresence> {
  return new Map(chats.map((presence) => [presence.session_id, presence]));
}

const read = (id: string, sequence: number, fields: Partial<AgentSessionPresence["reads"][number]> = {}) => ({
  id, sequence, tool_call_id: `call-${id}`, tool: "read", root_id: "r1", path: "a.go",
  extent: "range" as const, ranges: [{ start_line: 1, end_line: 2 }], stale: false, ...fields,
});

describe("agent chats", () => {
  it("keeps a chat's slot while another chat comes and goes, and returns it when the chat comes back", () => {
    const first = agentChats(sessions(chat("a", { title: "Fix login" }), chat("b")));
    expect(first.get("a")).toEqual({ sessionId: "a", title: "Fix login", slot: 0 });
    expect(first.get("b")?.title).toBe("Untitled chat");
    const withoutA = agentChats(sessions(chat("b"), chat("c")));
    expect(withoutA.get("b")?.slot).toBe(1);
    // The absent chat's slot stays free while other slots remain.
    expect(withoutA.get("c")?.slot).toBe(2);
    expect(agentChats(sessions(chat("a"), chat("b"))).get("a")?.slot).toBe(0);
  });
});

describe("agent files", () => {
  it("shows the strongest live state and lists every fact", () => {
    const files = projectAgentFiles(sessions(
      chat("a", {
        title: "Fix login",
        reads: [read("r", 1)],
        activities: [{ tool_call_id: "c", tool: "edit", root_id: "r1", path: "a.go", kind: "editing" }],
        intents: [{
          id: "i", tool_call_id: "c", tool: "edit", operation: "edit", state: "awaiting_approval",
          root_id: "r1", path: "a.go", extent: "range", ranges: [{ start_line: 3, end_line: 4 }],
        }],
      }),
      chat("b", {
        title: "Docs",
        worker_drafts: [{ worker_id: "j", state: "ready", root_id: "r1", path: "a.go", extent: "range", ranges: [], insertions: 3, deletions: 1 }],
      }),
    ), ALL, undefined);
    const file = files.get(agentFileKey("r1", "a.go"))!;
    expect(file.state).toBe("waiting");
    expect(file.stateChat?.title).toBe("Fix login");
    expect(file.highlight?.sessionId).toBe("a");
    expect(file.labels).toEqual([
      "Fix login: waiting for your approval to edit",
      "Docs: worker draft ready to land (+3 −1)",
      "Fix login: read this turn",
    ]);
  });

  it("follows the kinds setting and prefers the open chat for the name highlight", () => {
    const reader = chat("a", { title: "Reader", reads: [read("r", 1, { path: "shared.go" })] });
    const editor = chat("b", {
      title: "Editor",
      reads: [read("s", 1, { path: "shared.go" })],
      intents: [{ id: "i", tool_call_id: "c", tool: "edit", operation: "edit", state: "pending", root_id: "r1", path: "edit.go", extent: "range", ranges: [{ start_line: 1, end_line: 1 }] }],
    });
    const key = agentFileKey("r1", "shared.go");
    expect(projectAgentFiles(sessions(reader, editor), ALL, "b").get(key)?.highlight?.title).toBe("Editor");
    expect(projectAgentFiles(sessions(reader, editor), ALL, "a").get(key)?.highlight?.title).toBe("Reader");
    const changesOff = projectAgentFiles(sessions(reader, editor), { ...ALL, changes: false }, undefined);
    expect(changesOff.has(agentFileKey("r1", "edit.go"))).toBe(false);
    expect(projectAgentFiles(sessions(reader), { ...ALL, reads: false }, undefined).has(key)).toBe(false);
  });
});

describe("agent document marks", () => {
  it("retains every whole-file read and gives the newest range its caret", () => {
    const marks = agentDocumentMarks(sessions(chat("a", {
      reads: [
        read("whole-old", 1, { extent: "whole_file", ranges: [] }),
        read("range", 2, { ranges: [{ start_line: 1, end_line: 1 }, { start_line: 5, end_line: 6 }] }),
        read("whole-new", 3, { extent: "whole_file", ranges: [], stale: true }),
        read("matches", 4, { extent: "matches", ranges: [{ start_line: 9, end_line: 9, start_character: 2, end_character: 8 }] }),
        read("elsewhere", 5, { path: "b.go" }),
      ],
    })), ALL, undefined, "r1", "a.go");
    expect(marks.map((mark) => [mark.key, mark.kind, mark.caret, mark.stale])).toEqual([
      ["whole-old:whole", "whole", false, false],
      ["range:0", "read", false, false],
      ["range:1", "read", false, false],
      ["whole-new:whole", "whole", false, true],
      ["matches:0", "match", true, false],
    ]);
  });

  it("labels the first range of a pending change and recognizes insertions", () => {
    const marks = agentDocumentMarks(sessions(chat("a", {
      intents: [{
        id: "i", tool_call_id: "c", tool: "edit", operation: "edit", state: "pending", root_id: "r1", path: "a.go",
        extent: "range", ranges: [{ start_line: 2, end_line: 3 }, { start_line: 8, end_line: 8, start_character: 0, end_character: 0 }],
      }, {
        id: "d", tool_call_id: "c2", tool: "delete", operation: "delete", state: "awaiting_approval", root_id: "r1", path: "a.go",
        extent: "whole_file", ranges: [],
      }],
      worker_drafts: [
        { worker_id: "j", state: "ready", root_id: "r1", path: "a.go", extent: "range", ranges: [{ start_line: 10, end_line: 12 }] },
        { worker_id: "k", state: "drafting", root_id: "r1", path: "a.go", extent: "whole_file", ranges: [] },
      ],
    })), ALL, undefined, "r1", "a.go");
    expect(marks.map((mark) => [mark.kind, mark.label])).toEqual([
      ["pending", "about to edit"],
      ["insertion", undefined],
      ["whole-pending", "waiting for your approval to delete"],
      ["pending", "worker draft ready to land"],
    ]);
  });
});
