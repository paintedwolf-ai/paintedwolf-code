import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  firstRecentSessionByProject,
  prefetchProjectIds,
  warmSessionChatCache,
} from "./session-prefetch.ts";
import { clearSessionChatCacheForTests, getSessionChatCache } from "./session-chat-cache.ts";

describe("session-prefetch", () => {
  it("selects distinct recent project ids in activity order", () => {
    const recents = [
      { projectId: "p1", sessionId: "a", title: "A" },
      { projectId: "p2", sessionId: "b", title: "B" },
      { projectId: "p1", sessionId: "c", title: "C" },
      { projectId: "p3", sessionId: "d", title: "D" },
    ];
    expect(prefetchProjectIds(recents, 2)).toEqual(["p1", "p2"]);
  });

  it("maps project ids to their first recent session", () => {
    const recents = [
      { projectId: "p1", sessionId: "a", title: "A" },
      { projectId: "p2", sessionId: "b", title: "B" },
    ];
    expect(firstRecentSessionByProject(recents, ["p2", "p1"])).toEqual([
      { projectId: "p2", sessionId: "b" },
      { projectId: "p1", sessionId: "a" },
    ]);
  });

  it("retires a recent chat the host no longer has instead of warming it every boot", async () => {
    const onSessionGone = vi.fn();
    const gone = stubClient({ getSessionBootstrap: vi.fn(async () => { throw new LycaonApiError("missing", 404, "session_not_found"); }) });
    await warmSessionChatCache(gone, { projectId: "p1", sessionId: "deleted" }, onSessionGone, () => true);
    expect(onSessionGone).toHaveBeenCalledWith({ projectId: "p1", sessionId: "deleted" });

    onSessionGone.mockClear();
    const offline = stubClient({ getSessionBootstrap: vi.fn(async () => { throw new LycaonApiError("offline", 500, "internal_error"); }) });
    await warmSessionChatCache(offline, { projectId: "p1", sessionId: "live" }, onSessionGone, () => true);
    expect(onSessionGone).not.toHaveBeenCalled();
  });

  it("records transcript ordinal bounds when warming the session cache", async () => {
    clearSessionChatCacheForTests();
    const scope = { projectId: "p1", sessionId: "warmed" };
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => ({
        session: {
          id: "warmed",
          owner_person_id: "00000000-0000-4000-8000-000000000002",
          project_id: "p1",
          posture: "build" as const,
          status: "idle" as const,
          created_at: "t",
          activity_at: "t",
          updated_at: "t",
        },
        transcript: {
          messages: [],
          watermark: 120,
          before_cursor: "older",
          ord_bounds: { first_ord: 1, last_ord: 120 },
          turn_clocks: {},
          turn_loads: {},
        },
      })),
    });
    await warmSessionChatCache(client, scope, vi.fn(), () => true);
    expect(getSessionChatCache(scope)?.transcript.bounds).toEqual({ firstOrd: 1, lastOrd: 120 });
    clearSessionChatCacheForTests();
  });
});
