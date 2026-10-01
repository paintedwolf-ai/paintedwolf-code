import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  firstRecentSessionByProject,
  prefetchProjectIds,
  warmSessionChatCache,
} from "./session-prefetch.ts";

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
    await warmSessionChatCache(gone, { projectId: "p1", sessionId: "deleted" }, [], onSessionGone);
    expect(onSessionGone).toHaveBeenCalledWith({ projectId: "p1", sessionId: "deleted" });

    onSessionGone.mockClear();
    const offline = stubClient({ getSessionBootstrap: vi.fn(async () => { throw new LycaonApiError("offline", 500, "internal_error"); }) });
    await warmSessionChatCache(offline, { projectId: "p1", sessionId: "live" }, [], onSessionGone);
    expect(onSessionGone).not.toHaveBeenCalled();
  });
});
