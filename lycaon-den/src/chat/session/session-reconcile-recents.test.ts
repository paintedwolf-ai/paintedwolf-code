import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { reconcileRecents, reconcileRecentsWithStores } from "./session-reconcile.ts";
import { LycaonApiError } from "../../api/http.ts";

describe("reconcileRecents", () => {
  it("drops rows whose sessions return 404", async () => {
    const client = stubClient({
      getSession: vi.fn(async (id: string) => {
        if (id === "gone") {
          throw new LycaonApiError("missing", 404, "session_not_found");
        }
        return { id };
      }),
    });

    const result = await reconcileRecents(client, [
      { projectId: "proj-a", sessionId: "live", title: "Live" },
      { projectId: "proj-a", sessionId: "gone", title: "Gone" },
    ]);

    expect(result.kept).toEqual([
      { projectId: "proj-a", sessionId: "live", title: "Live" },
    ]);
    expect(result.gone).toEqual([
      { projectId: "proj-a", sessionId: "gone" },
    ]);
  });

  it("keeps rows when the probe fails for a non-404 reason", async () => {
    const row = { projectId: "proj-a", sessionId: "maybe", title: "Maybe" };
    const client = stubClient({
      getSession: vi.fn(async () => {
        throw new LycaonApiError("offline", 500, "internal_error");
      }),
    });

    const result = await reconcileRecents(client, [row]);
    expect(result).toEqual({ kept: [row], gone: [] });
  });
});

describe("reconcileRecentsWithStores", () => {
  it("does not prune recents after the backend changes during the read", async () => {
    let rejectRead!: (error: Error) => void;
    const client = stubClient({ getSession: () => new Promise((_resolve, reject) => { rejectRead = reject; }) });
    const rows = [{ projectId: "project", sessionId: "gone", title: "Gone", lastActivityAt: 1 }];
    const replaceRecents = vi.fn(async () => undefined);
    const shell = { evictConversation: vi.fn() };
    let current = true;
    const reading = reconcileRecentsWithStores(client, { state: { loaded: true, recents: rows }, replaceRecents }, shell, () => current);
    current = false;
    rejectRead(new LycaonApiError("Missing", 404, "session_not_found"));
    await reading;
    expect(replaceRecents).not.toHaveBeenCalled();
    expect(shell.evictConversation).not.toHaveBeenCalled();
  });

  it("does not evict a new scope while old pruning persists", async () => {
    const client = stubClient({ getSession: async () => { throw new LycaonApiError("Missing", 404, "session_not_found"); } });
    const rows = [{ projectId: "project", sessionId: "gone", title: "Gone", lastActivityAt: 1 }];
    let finish!: () => void;
    const replaceRecents = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    const shell = { evictConversation: vi.fn() };
    let current = true;
    const reading = reconcileRecentsWithStores(client, { state: { loaded: true, recents: rows }, replaceRecents }, shell, () => current);
    await vi.waitFor(() => expect(replaceRecents).toHaveBeenCalledOnce());
    current = false;
    finish();
    await reading;
    expect(shell.evictConversation).not.toHaveBeenCalled();
  });

  it("persists pruned recents and evicts shell foreground", async () => {
    const client = stubClient({
      getSession: vi.fn(async (id: string) => {
        if (id === "gone") {
          throw new LycaonApiError("missing", 404, "session_not_found");
        }
        return { id };
      }),
    });

    const recents = createRecentsStore();
    await recents.load();
    await recents.registerSession({
      projectId: "proj-a",
      sessionId: "live",
      title: "Live",
    });
    await recents.registerSession({
      projectId: "proj-a",
      sessionId: "gone",
      title: "Gone",
    });
    const shell = createShellStore("connected");
    shell.commitStageScope({ projectId: "proj-a", sessionId: "gone" });

    const gone = await reconcileRecentsWithStores(client, recents, shell, () => true);

    expect(gone).toEqual([{ projectId: "proj-a", sessionId: "gone" }]);
    expect(recents.state.recents.map((r) => r.sessionId)).toEqual(["live"]);
    expect(shell.state.foreground).toBeNull();
  });
});
