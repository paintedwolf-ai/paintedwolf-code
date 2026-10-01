import { afterEach, describe, expect, it } from "vitest";
import type { AgentPresenceSnapshot, AgentSessionPresence } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  agentPresenceFor,
  applyAgentPresenceEvent,
  applyAgentPresenceSnapshot,
  resetAgentPresenceForTests,
  resyncAgentPresence,
  retainAgentPresenceProject,
} from "./agent-presence-store.ts";

const PROJECT = "project-1";

function presence(sessionId: string, path?: string): AgentSessionPresence {
  return {
    session_id: sessionId, turn: 1, reads: [], intents: [], worker_drafts: [],
    activities: path ? [{ tool_call_id: "call", tool: "read", root_id: "r1", path, kind: "reading" }] : [],
  };
}

function event(sessionId: string, revision: number, path?: string) {
  return { project_id: PROJECT, session_id: sessionId, revision, presence: presence(sessionId, path) };
}

function paths(): Record<string, string | undefined> {
  return Object.fromEntries([...agentPresenceFor(PROJECT)].map(([id, value]) => [id, value.activities[0]?.path]));
}

afterEach(() => resetAgentPresenceForTests());

describe("agent presence store", () => {
  it("replaces a chat's presence with newer events and drops empty chats", () => {
    applyAgentPresenceEvent(event("s1", 2, "a.ts"));
    applyAgentPresenceEvent(event("s1", 1, "stale.ts"));
    expect(paths()).toEqual({ s1: "a.ts" });
    applyAgentPresenceEvent(event("s1", 3));
    expect(agentPresenceFor(PROJECT).size).toBe(0);
  });

  it("keeps events newer than a snapshot that was loading when they arrived", () => {
    applyAgentPresenceEvent(event("s1", 5, "newer.ts"));
    applyAgentPresenceEvent(event("s2", 6));
    const snapshot: AgentPresenceSnapshot = {
      project_id: PROJECT, revision: 4,
      sessions: [presence("s1", "older.ts"), presence("s2", "gone.ts"), presence("s3", "c.ts")],
    };
    applyAgentPresenceSnapshot(snapshot);
    expect(paths()).toEqual({ s1: "newer.ts", s3: "c.ts" });
    applyAgentPresenceEvent(event("s3", 4, "already-reflected.ts"));
    expect(paths()).toEqual({ s1: "newer.ts", s3: "c.ts" });
  });

  it("accepts a restarted host's lower revisions after a resync", async () => {
    applyAgentPresenceEvent(event("s1", 40, "before-restart.ts"));
    const client = stubClient({
      getAgentPresence: async () => ({ project_id: PROJECT, revision: 0, sessions: [] }),
    });
    await resyncAgentPresence(client, PROJECT);
    expect(agentPresenceFor(PROJECT).size).toBe(0);
    applyAgentPresenceEvent(event("s1", 1, "after-restart.ts"));
    expect(paths()).toEqual({ s1: "after-restart.ts" });
  });

  it("forgets projects the window no longer follows", () => {
    applyAgentPresenceEvent(event("s1", 1, "a.ts"));
    applyAgentPresenceEvent({ ...event("s2", 1, "b.ts"), project_id: "project-2" });
    retainAgentPresenceProject("project-2");
    expect(agentPresenceFor(PROJECT).size).toBe(0);
    expect(agentPresenceFor("project-2").size).toBe(1);
  });
});
