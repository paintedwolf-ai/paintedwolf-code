import { describe, expect, it } from "vitest";
import { SESSION_CREATE_PENDING_ID } from "../chat/session/session-scope.ts";
import { deriveStageScope, holdPresentedChat } from "./stage-scope.ts";
import type { Session } from "../api/types.ts";

describe("deriveStageScope", () => {
  it("home when no active project", () => {
    expect(
      deriveStageScope(
        { activeProjectId: null, foreground: null, opening: null },
        { currentSession: undefined, chatHydrationLock: undefined },
      ),
    ).toEqual({
      project_id: null,
      session_id: null,
      phase: "home",
    });
  });

  it("opening while a workspace is ahead of its project id", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: null,
          foreground: null,
          opening: { token: 1, label: "widgets" },
        },
        { currentSession: undefined, chatHydrationLock: "*" },
      ),
    ).toEqual({
      project_id: null,
      session_id: null,
      phase: "opening",
    });
  });

  it("switching when intent project differs from foreground project", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: "proj-b",
          foreground: { projectId: "proj-a", sessionId: "sess-a" },
          opening: null,
        },
        {
          currentSession: { id: "sess-a", project_id: "proj-a" } as Session,
          chatHydrationLock: undefined,
        },
      ),
    ).toEqual({
      project_id: "proj-b",
      session_id: null,
      phase: "switching",
    });
  });

  it("switching during cross-project wildcard hydration lock", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: "proj-b",
          foreground: { projectId: "proj-b", sessionId: "sess-b" },
          opening: null,
        },
        {
          currentSession: undefined,
          chatHydrationLock: "*",
        },
      ),
    ).toEqual({
      project_id: "proj-b",
      session_id: null,
      phase: "switching",
    });
  });

  it("switching while session create is pending", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: "proj-a",
          foreground: {
            projectId: "proj-a",
            sessionId: SESSION_CREATE_PENDING_ID,
          },
          opening: null,
        },
        { currentSession: undefined, chatHydrationLock: "*" },
      ),
    ).toEqual({
      project_id: "proj-a",
      session_id: null,
      phase: "switching",
    });
  });

  it("ready when foreground matches intent and current session", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: "proj-a",
          foreground: { projectId: "proj-a", sessionId: "sess-a" },
          opening: null,
        },
        {
          currentSession: { id: "sess-a", project_id: "proj-a" } as Session,
          chatHydrationLock: undefined,
        },
      ),
    ).toEqual({
      project_id: "proj-a",
      session_id: "sess-a",
      phase: "ready",
    });
  });

  it("ready during same-project resume while session-scoped lock is set", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: "proj-a",
          foreground: { projectId: "proj-a", sessionId: "sess-b" },
          opening: null,
        },
        {
          currentSession: { id: "sess-a", project_id: "proj-a" } as Session,
          chatHydrationLock: "sess-b",
        },
      ),
    ).toEqual({
      project_id: "proj-a",
      session_id: "sess-b",
      phase: "ready",
    });
  });

  it("switching when foreground is set but no session is hydrated yet", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: "proj-a",
          foreground: { projectId: "proj-a", sessionId: "sess-a" },
          opening: null,
        },
        { currentSession: undefined, chatHydrationLock: undefined },
      ),
    ).toEqual({
      project_id: "proj-a",
      session_id: null,
      phase: "switching",
    });
  });

  it("project-empty when active project has no foreground session", () => {
    expect(
      deriveStageScope(
        { activeProjectId: "proj-a", foreground: null, opening: null },
        { currentSession: undefined, chatHydrationLock: undefined },
      ),
    ).toEqual({
      project_id: "proj-a",
      session_id: null,
      phase: "project-empty",
    });
  });

  it("treats a currentSession from another project as switching", () => {
    expect(
      deriveStageScope(
        {
          activeProjectId: "proj-b",
          foreground: { projectId: "proj-b", sessionId: "sess-b" },
          opening: null,
        },
        {
          currentSession: { id: "sess-a", project_id: "proj-a" } as Session,
          chatHydrationLock: undefined,
        },
      ),
    ).toEqual({
      project_id: "proj-b",
      session_id: null,
      phase: "switching",
    });
  });
});

describe("holdPresentedChat", () => {
  const outgoing = { projectId: "p1", sessionId: "s1" };
  const incoming = { projectId: "p1", sessionId: "s2" };

  it("holds the outgoing chat until the incoming session is bound", () => {
    expect(
      holdPresentedChat(outgoing, incoming, {
        currentSessionId: "s1",
        transcriptSessionId: "s1",
        hydrationLock: "s2",
        projectId: "p1",
      }),
    ).toEqual(outgoing);
  });

  it("presents the incoming chat once currentSession and transcript match", () => {
    expect(
      holdPresentedChat(outgoing, incoming, {
        currentSessionId: "s2",
        transcriptSessionId: "s2",
        hydrationLock: "s2",
        projectId: "p1",
      }),
    ).toEqual(incoming);
  });

  it("holds through a same-project create that has no ready incoming chat", () => {
    expect(
      holdPresentedChat(outgoing, null, {
        currentSessionId: undefined,
        transcriptSessionId: undefined,
        hydrationLock: "*",
        projectId: "p1",
      }),
    ).toEqual(outgoing);
  });

  it("does not carry a chat into another project", () => {
    expect(
      holdPresentedChat(outgoing, null, {
        currentSessionId: undefined,
        transcriptSessionId: undefined,
        hydrationLock: "*",
        projectId: "p2",
      }),
    ).toBeNull();
  });
});
