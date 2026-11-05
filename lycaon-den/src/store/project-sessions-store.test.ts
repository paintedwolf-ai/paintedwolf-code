import { afterEach, describe, expect, it, vi } from "vitest";
import {
  SIDEBAR_CHATS_FETCH_LIMIT,
  createProjectSessionsStore,
} from "./project-sessions-store.ts";
import type { ProjectBackgroundEvent, SessionListPage, SessionSummary } from "../api/types.ts";

type ListOpts = { pinned?: boolean; sort?: string; limit?: number };

function row(id: string, pinRank?: number): SessionSummary {
  return { id, project_id: "a", title: id, ...(pinRank != null ? { pin_rank: pinRank } : {}) } as unknown as SessionSummary;
}

/** Answers the pinned and unpinned queries from one host inventory. */
function hostClient(rows: () => SessionSummary[]) {
  return {
    listProjectSessions: vi.fn(async (_projectId: string, opts: ListOpts) => {
      const matched = rows().filter((r) => (r.pin_rank != null) === opts.pinned);
      return { sessions: matched, total: matched.length } satisfies Partial<SessionListPage>;
    }),
  };
}

async function loadedStore(client: ReturnType<typeof hostClient>) {
  const store = createProjectSessionsStore(() => client as never, { onSessionEvent: () => () => {} });
  store.setProject("a");
  await vi.waitFor(() => expect(store.state.loaded).toBe(true));
  return store;
}

describe("sidebar inventory", () => {
  it("fetches every pin and the first chats in the chosen order", async () => {
    const client = hostClient(() => [row("pin", 1), row("chat")]);
    const store = await loadedStore(client);

    expect(client.listProjectSessions).toHaveBeenCalledWith("a", { pinned: true, sort: "pin", limit: 200 });
    expect(client.listProjectSessions).toHaveBeenCalledWith("a", {
      pinned: false, sort: "created", limit: SIDEBAR_CHATS_FETCH_LIMIT,
    });
    expect(store.state.rows.map((r) => r.id)).toEqual(["pin", "chat"]);
    expect(store.state.total).toBe(2);
  });

  it("lists a chat once when the two pages disagree about its pin", async () => {
    const client = {
      listProjectSessions: vi.fn(async (_projectId: string, opts: ListOpts) =>
        opts.pinned
          ? { sessions: [row("moving", 1)], total: 1 }
          : { sessions: [row("moving"), row("other")], total: 2 }),
    };
    const store = await loadedStore(client as unknown as ReturnType<typeof hostClient>);
    expect(store.state.rows.map((r) => r.id)).toEqual(["moving", "other"]);
  });

  it("refetches unpinned chats when the order changes", async () => {
    const client = hostClient(() => [row("chat")]);
    const store = await loadedStore(client);

    store.setSort("activity");
    await vi.waitFor(() =>
      expect(client.listProjectSessions).toHaveBeenCalledWith("a", {
        pinned: false, sort: "activity", limit: SIDEBAR_CHATS_FETCH_LIMIT,
      }),
    );
    expect(store.state.sort).toBe("activity");
  });

  it("shows a created session at once and keeps it over the next reload", async () => {
    const hostRows: SessionSummary[] = [];
    const store = await loadedStore(hostClient(() => hostRows));

    const created = row("s-new");
    store.insertRow(created);
    expect(store.state.rows.map((r) => r.id)).toEqual(["s-new"]);
    expect(store.state.total).toBe(1);
    store.insertRow(created);
    expect(store.state.rows).toHaveLength(1);
    store.insertRow({ id: "s-other", project_id: "p2" } as unknown as SessionSummary);
    expect(store.state.rows).toHaveLength(1);

    hostRows.push(created);
    await store.refresh();
    expect(store.state.rows.map((r) => r.id)).toEqual(["s-new"]);
  });
});

describe("pin edits", () => {
  it("shows a pin at once and keeps it over a reload until the host answers", async () => {
    const host = { rank: undefined as number | undefined };
    const store = await loadedStore(hostClient(() => [row("pinned", 1), row("s1", host.rank)]));
    expect(store.nextPinRank()).toBe(2);

    let accept!: () => void;
    const pinning = store.applyRowPatches(new Map([["s1", { pin_rank: 2 }]]), () =>
      new Promise<void>((resolve) => {
        accept = resolve;
      }),
    );
    expect(store.pinnedIds()).toEqual(["pinned", "s1"]);

    await store.refresh();
    expect(store.pinnedIds()).toEqual(["pinned", "s1"]);

    host.rank = 2;
    accept();
    await pinning;
    await store.refresh();
    expect(store.pinnedIds()).toEqual(["pinned", "s1"]);
  });

  it("shows a moved order across rows and restores the host's on failure", async () => {
    const store = await loadedStore(hostClient(() => [row("a1", 1), row("a2", 2), row("a3", 3)]));
    let fail!: (error: Error) => void;
    const moving = store.applyRowPatches(
      new Map([["a3", { pin_rank: 1 }], ["a1", { pin_rank: 2 }], ["a2", { pin_rank: 3 }]]),
      () => new Promise<void>((_resolve, reject) => {
        fail = reject;
      }),
    );
    expect(store.pinnedIds()).toEqual(["a3", "a1", "a2"]);

    fail(new Error("offline"));
    await expect(moving).rejects.toThrow("offline");
    expect(store.pinnedIds()).toEqual(["a1", "a2", "a3"]);
  });
});

describe("background work counts", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  async function backgroundStore(rows: () => SessionSummary[]) {
    const client = hostClient(rows);
    let emit!: (ev: ProjectBackgroundEvent) => void;
    const store = createProjectSessionsStore(() => client as never, {
      onSessionEvent: () => () => {},
      onProjectBackgroundEvent: (cb) => {
        emit = cb;
        return () => {};
      },
    });
    store.setProject("a");
    await vi.waitFor(() => expect(store.state.loaded).toBe(true));
    // Debounced reloads wait for runAllTimers, so counts read here are the event's own.
    vi.useFakeTimers();
    const event = (action: ProjectBackgroundEvent["action"], session_id: string, project_id = "a") =>
      emit({ action, session_id, project_id } as ProjectBackgroundEvent);
    const running = () => store.state.rows.find((r) => r.id === "chat")?.background_running;
    return { client, store, event, running };
  }

  it("counts started and ended work on the chat's row without going below zero", async () => {
    const { event, running } = await backgroundStore(() => [row("chat")]);
    event("started", "chat");
    event("started", "chat");
    expect(running()).toBe(2);
    event("ended", "chat");
    expect(running()).toBe(1);
    event("ended", "chat");
    event("ended", "chat");
    expect(running()).toBe(0);
  });

  it("leaves counts alone for other actions, unlisted chats and other projects", async () => {
    const { client, event, running } = await backgroundStore(() => [row("chat")]);
    const calls = client.listProjectSessions.mock.calls.length;
    event("started", "chat", "another-project");
    await vi.runAllTimersAsync();
    expect(running()).toBeUndefined();
    expect(client.listProjectSessions.mock.calls.length).toBe(calls);

    event("ports_changed", "chat");
    event("started", "unlisted");
    event("started", "   ");
    expect(running()).toBeUndefined();
  });

  it("settles on the host's count at the reload the event schedules", async () => {
    let hostCount = 0;
    const { store, event, running } = await backgroundStore(() => [
      { ...row("chat"), background_running: hostCount },
    ]);
    event("started", "chat");
    expect(running()).toBe(1);
    hostCount = 4;
    await vi.runAllTimersAsync();
    vi.useRealTimers();
    await vi.waitFor(() => expect(running()).toBe(4));
    expect(store.state.rows.map((r) => r.id)).toEqual(["chat"]);
  });
});

describe("project session presentation", () => {
  it("settles a failed inventory so opening the workspace remains usable and retry can recover", async () => {
    const client = {
      listProjectSessions: vi.fn()
        .mockRejectedValueOnce(new Error("Unavailable"))
        .mockResolvedValue({ sessions: [], total: 0 }),
    };
    const store = createProjectSessionsStore(() => client as never, { onSessionEvent: () => () => {} });
    store.setProject("a");
    await vi.waitFor(() => expect(store.state.loaded).toBe(true));
    expect(store.state.error).toBe("Unavailable");
    await store.refresh();
    expect(store.state.error).toBeUndefined();
    expect(store.state.total).toBe(0);
  });

  it("never installs an outgoing project's late inventory", async () => {
    let finish!: (page: SessionListPage) => void;
    const client = {
      listProjectSessions: vi.fn().mockImplementation((projectId: string, opts: ListOpts) => {
        if (projectId === "a" && opts.pinned === false) {
          return new Promise<SessionListPage>((resolve) => {
            finish = resolve;
          });
        }
        return Promise.resolve({ sessions: [], total: opts.pinned ? 2 : 5 });
      }),
    };
    const store = createProjectSessionsStore(() => client as never, { onSessionEvent: () => () => {} });
    store.setProject("a");
    await Promise.resolve();
    store.setProject("b");
    await vi.waitFor(() => expect(store.state.total).toBe(7));
    finish({ sessions: [], total: 42 });
    await Promise.resolve();
    await Promise.resolve();
    expect(store.state.projectId).toBe("b");
    expect(store.state.total).toBe(7);
  });
});
