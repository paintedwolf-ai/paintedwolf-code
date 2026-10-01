import { stubClient } from "../test/client-fixture.ts";
import type { Project } from "../api/types.ts";
import { describe, expect, it, vi } from "vitest";
import { errorOf, isLoaded, isResolved } from "./load-state.ts";
import { createProjectsStore, type SseBus } from "./projects-store.ts";
import { wireProject } from "../api/mocks/project-fixture.ts";

function createBus(): { bus: SseBus; emit: (ev: any) => void } {
  let cb: ((ev: any) => void) | undefined;
  return {
    bus: {
      onProjectEvent(next) {
        cb = next;
        return () => {
          cb = undefined;
        };
      },
    },
    emit(ev) {
      cb?.(ev);
    },
  };
}

describe("projects-store pending edits", () => {
  it("shows a star at once and keeps it over a stale host update until the host answers", async () => {
    const { bus, emit } = createBus();
    const store = createProjectsStore(() => null, bus);
    const host = { ...wireProject("/a", "p1"), starred: false };
    store.load([host]);
    let answer!: (project: typeof host) => void;
    const starring = store.applyPatch("p1", { starred: true }, () =>
      new Promise((resolve) => {
        answer = resolve;
      }),
    );
    expect(store.byId("p1")?.starred).toBe(true);

    emit({ action: "updated", id: "p1", project: host });
    expect(store.byId("p1")?.starred).toBe(true);

    answer({ ...host, starred: true });
    await starring;
    expect(store.byId("p1")?.starred).toBe(true);
    emit({ action: "updated", id: "p1", project: { ...host, starred: false } });
    expect(store.byId("p1")?.starred).toBe(false);
  });

  it("restores the host row and rethrows when the edit fails", async () => {
    const { bus } = createBus();
    const store = createProjectsStore(() => null, bus);
    const host = { ...wireProject("/a", "p1"), name: "Original" };
    store.load([host]);

    const renaming = store.applyPatch("p1", { name: "Renamed" }, () =>
      Promise.reject(new Error("offline")),
    );
    expect(store.byId("p1")?.name).toBe("Renamed");
    await expect(renaming).rejects.toThrow("offline");
    expect(store.byId("p1")?.name).toBe("Original");
  });
});

describe("projects-store", () => {
  it("hydrates projects from listProjects", async () => {
    const { bus } = createBus();
    const client = {
      listProjects: async () => [wireProject("/a", "p1"), wireProject("/b", "p2")],
    } as any;
    const store = createProjectsStore(() => client, bus);
    await store.hydrate();
    expect(isLoaded(store.state.registry)).toBe(true);
    expect(store.state.projects.map((p) => p.id)).toEqual(["p1", "p2"]);
  });

  it("load replaces registry from connect path", () => {
    const { bus } = createBus();
    const client = {
      listProjects: async () => [wireProject("/a", "p1"), wireProject("/b", "p2")],
    } as any;
    const store = createProjectsStore(() => client, bus);
    store.load([wireProject("/c", "p3")]);
    expect(isLoaded(store.state.registry)).toBe(true);
    expect(store.state.projects.map((p) => p.id)).toEqual(["p3"]);
  });

  it("seeds stale registry rows before server hydrate", () => {
    const { bus } = createBus();
    const store = createProjectsStore(() => null, bus);
    store.seedFromSnapshot([
      {
        id: "p1",
        name: "Cached",
        roots: [
          {
            id: "r1",
            path: "/tmp/p",
            label: "p",
            is_primary: true,
            added_at: "2025-01-01T00:00:00Z",
            kind: "attached",
          },
        ],
        roots_generation: 0,
        session_count: 2,
        starred: false,
        is_draft: false,
        promotion: null,
        last_opened_at: "2025-01-02T00:00:00Z",
        created_at: "2025-01-01T00:00:00Z",
      },
    ]);
    expect(isLoaded(store.state.registry)).toBe(false);
    expect(store.summary("p1")?.displayName).toBe("Cached");
  });

  it("resolves a failed hydrate so the shell nav does not hang", async () => {
    const { bus } = createBus();
    const client = {
      listProjects: async () => {
        throw new Error("backend restarted");
      },
    } as any;
    const store = createProjectsStore(() => client, bus);
    store.seedFromSnapshot([
      {
        id: "p1",
        name: "Cached",
        roots: [],
        roots_generation: 0,
        session_count: 0,
        starred: false,
        is_draft: false,
        promotion: null,
        last_opened_at: "2025-01-02T00:00:00Z",
        created_at: "2025-01-01T00:00:00Z",
      },
    ]);
    await store.hydrate();
    // Cache rows can render; empty-state copy still waits for the registry.
    expect(isResolved(store.state.registry)).toBe(true);
    expect(isLoaded(store.state.registry)).toBe(false);
    expect(store.state.projects.map((p) => p.id)).toEqual(["p1"]);
  });

  it("resolves when no backend client is available", async () => {
    const { bus } = createBus();
    const store = createProjectsStore(() => null, bus);
    store.seedFromSnapshot([
      {
        id: "p1",
        name: "Cached",
        roots: [],
        roots_generation: 0,
        session_count: 0,
        starred: false,
        is_draft: false,
        promotion: null,
        last_opened_at: "2025-01-02T00:00:00Z",
        created_at: "2025-01-01T00:00:00Z",
      },
    ]);
    await store.hydrate();
    expect(isResolved(store.state.registry)).toBe(true);
    expect(isLoaded(store.state.registry)).toBe(false);
  });

  it("applies project.created and project.deleted SSE updates", async () => {
    const { bus, emit } = createBus();
    const client = { listProjects: async () => [] } as any;
    const store = createProjectsStore(() => client, bus);
    await store.hydrate();
    emit({ id: "p1", action: "created", project: wireProject("/a", "p1") });
    emit({ id: "p1", action: "deleted" });
    expect(store.state.projects).toEqual([]);
  });

  it("drop syncs cached projects to disk when loaded", async () => {
    const { bus } = createBus();
    const client = {
      listProjects: async () => [wireProject("/a", "p1"), wireProject("/b", "p2")],
    } as any;
    const store = createProjectsStore(() => client, bus);
    await store.hydrate();
    store.drop("p1");
    expect(store.state.projects.map((p) => p.id)).toEqual(["p2"]);
    const { getAppStateSnapshot } = await import("./app-state-snapshot.ts");
    expect(getAppStateSnapshot().cachedProjects?.map((p) => p.id)).toEqual(["p2"]);
  });

  it("a failed hydrate records the error instead of settling an empty registry", async () => {
    const { bus } = createBus();
    const client = {
      listProjects: async () => {
        throw new Error("sidecar unreachable");
      },
    } as any;
    const store = createProjectsStore(() => client, bus);
    await store.hydrate();
    // First-run copy waits for a settled registry.
    expect(isLoaded(store.state.registry)).toBe(false);
    expect(errorOf(store.state.registry)).toBe("sidecar unreachable");
  });

  it("a missing client records a failure rather than a settled empty registry", async () => {
    const { bus } = createBus();
    const store = createProjectsStore(() => null, bus);
    await store.hydrate();
    expect(store.state.registry.state).toBe("error");
  });
});


describe("project request ordering", () => {
  it.each(["older first", "newer first"])("keeps the latest edit when responses finish %s", async (order) => {
    const { bus } = createBus();
    const store = createProjectsStore(() => null, bus);
    const host = wireProject("/a", "p1");
    store.load([host]);
    let answerOld!: (project: Project) => void;
    let answerNew!: (project: Project) => void;
    const old = store.applyPatch("p1", { name: "Old" }, () => new Promise((resolve) => { answerOld = resolve; }));
    const fresh = store.applyPatch("p1", { name: "New" }, () => new Promise((resolve) => { answerNew = resolve; }));
    if (order === "older first") {
      answerOld({ ...host, name: "Old" });
      await old;
      expect(store.byId("p1")?.name).toBe("New");
      answerNew({ ...host, name: "New" });
    } else {
      answerNew({ ...host, name: "New" });
      await fresh;
      answerOld({ ...host, name: "Old" });
    }
    await Promise.all([old, fresh]);
    expect(store.byId("p1")?.name).toBe("New");
  });

  it("rolls a failed latest edit back to the preceding confirmed response", async () => {
    const { bus } = createBus();
    const store = createProjectsStore(() => null, bus);
    const host = wireProject("/a", "p1");
    store.load([host]);
    let answerOld!: (project: Project) => void;
    let rejectNew!: (error: Error) => void;
    const old = store.applyPatch("p1", { name: "Confirmed" }, () => new Promise((resolve) => { answerOld = resolve; }));
    const fresh = store.applyPatch("p1", { name: "Failed" }, () => new Promise((_resolve, reject) => { rejectNew = reject; }));
    answerOld({ ...host, name: "Confirmed" });
    await old;
    rejectNew(new Error("offline"));
    await expect(fresh).rejects.toThrow("offline");
    expect(store.byId("p1")?.name).toBe("Confirmed");
  });

  it("installs an earlier successful edit after the latest edit fails first", async () => {
    const { bus } = createBus();
    const store = createProjectsStore(() => null, bus);
    const host = wireProject("/a", "p1");
    store.load([host]);
    let answerOld!: (project: Project) => void;
    let rejectNew!: (error: Error) => void;
    const old = store.applyPatch("p1", { name: "Confirmed" }, () => new Promise((resolve) => { answerOld = resolve; }));
    const fresh = store.applyPatch("p1", { name: "Failed" }, () => new Promise((_resolve, reject) => { rejectNew = reject; }));
    rejectNew(new Error("offline"));
    await expect(fresh).rejects.toThrow("offline");
    answerOld({ ...host, name: "Confirmed" });
    await old;
    expect(store.byId("p1")?.name).toBe("Confirmed");
  });

  it("does not resurrect a deleted project when its mutation returns", async () => {
    const { bus, emit } = createBus();
    const store = createProjectsStore(() => null, bus);
    const host = wireProject("/a", "p1");
    store.load([host]);
    let answer!: (project: Project) => void;
    const changing = store.applyPatch("p1", { name: "Updated" }, () => new Promise((resolve) => { answer = resolve; }));
    emit({ id: "p1", action: "deleted" });
    answer({ ...host, name: "Updated" });
    await changing;
    expect(store.byId("p1")).toBeUndefined();
  });

  it("re-reads a list superseded by a project event", async () => {
    const { bus, emit } = createBus();
    const host = wireProject("/a", "p1");
    let answer!: (projects: Project[]) => void;
    const listProjects = vi.fn().mockImplementationOnce(() => new Promise<Project[]>((resolve) => { answer = resolve; })).mockResolvedValue([]);
    const client = stubClient({ listProjects });
    const store = createProjectsStore(() => client, bus);
    store.load([host]);
    const reading = store.hydrate();
    emit({ id: "p1", action: "deleted" });
    answer([host]);
    await reading;
    expect(store.state.projects).toEqual([]);
    expect(listProjects).toHaveBeenCalledTimes(2);
    expect(isLoaded(store.state.registry)).toBe(true);
  });

  it("keeps a new connection's rows when the old list finishes", async () => {
    const { bus } = createBus();
    let answer!: (projects: Project[]) => void;
    let client = stubClient({ listProjects: () => new Promise<Project[]>((resolve) => { answer = resolve; }) });
    const store = createProjectsStore(() => client, bus);
    const old = store.hydrate();
    client = stubClient({ listProjects: async () => [wireProject("/new", "new")] });
    await store.hydrate();
    answer([wireProject("/old", "old")]);
    await old;
    expect(store.state.projects.map((project) => project.id)).toEqual(["new"]);
  });
});
