// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { connectionServicesFixture } from "../../test/connection-services-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { withBootstrap } from "../../chat/session/session-lifecycle-test-fixture.ts";
import { clearSessionChatCacheForTests, getSessionChatCache } from "../../chat/session/session-chat-cache.ts";
import { prefetchRecentSessionCaches } from "../../chat/session/session-prefetch.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { SessionBootstrap } from "../../api/types.ts";
import { installConnectionFixtureCleanup, loadModule, createLycaonClient } from "./app-connection-test-fixture.ts";

installConnectionFixtureCleanup();

it.each(["binding", "generation", "current"] as const)("fences deferred prefetch success and retirement against the %s lifetime", async (change) => {
  const mod = await loadModule();
  for (const outcome of ["success", "gone"] as const) {
    clearSessionChatCacheForTests();
    const scope = { projectId: "project", sessionId: "session" };
    const fixture = withBootstrap({
      getSession: async () => ({ id: scope.sessionId, project_id: scope.projectId, workspace_path: "/project", owner_person_id: "person", posture: "build", status: "idle", created_at: "t", updated_at: "t", activity_at: "t" }),
      listSessionMessages: async () => ({ messages: [], watermark: 1, turn_clocks: {}, turn_loads: {} }),
    });
    const payload = await fixture.getSessionBootstrap(scope.sessionId);
    let resolve!: (value: SessionBootstrap) => void;
    let reject!: (error: Error) => void;
    const getSessionBootstrap = vi.fn(() => new Promise<SessionBootstrap>((yes, no) => { resolve = yes; reject = no; }));
    const client = stubClient({ getSessionBootstrap });
    const services = connectionServicesFixture();
    const binding = mod.bindAppConnection(services);
    mod.setLycaonClientForTest(client);
    const connectionCurrent = mod.captureConnectionGuard(client);
    const recents = [{ ...scope, title: "Recent" }, ...(change === "current" ? [] : [{ projectId: "other", sessionId: "other", title: "Other" }])];
    const pending = prefetchRecentSessionCaches(client, recents, mod.handleSessionGone,
      () => !binding.signal.aborted && connectionCurrent());
    const replacement = connectionServicesFixture();
    const retired = vi.spyOn(change === "binding" ? replacement.retire : services.retire, "session");
    let dispose = binding.dispose;
    if (change === "binding") dispose = mod.bindAppConnection(replacement).dispose;
    if (change === "generation") {
      createLycaonClient.mockReturnValue(client);
      mod.attachKnownBackend(services.app, { baseUrl: "http://replacement", apiToken: "new" });
    }
    if (outcome === "success") resolve(payload);
    else reject(new LycaonApiError("missing", 404, "session_not_found"));
    await pending;
    expect(getSessionBootstrap).toHaveBeenCalledOnce();
    expect(Boolean(getSessionChatCache(scope))).toBe(change === "current" && outcome === "success");
    expect(retired).toHaveBeenCalledTimes(change === "current" && outcome === "gone" ? 1 : 0);
    dispose();
  }
});
