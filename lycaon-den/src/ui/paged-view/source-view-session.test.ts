import { expect, it, vi } from "vitest";
import { createSourceViewsClient as wireClient } from "../../api/source-views-client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { SourceTreeView } from "../../api/types.ts";
import { endSourceViewsForChat, receiveSourceViewEvent, resyncSourceViews, SourceViewSession } from "./source-view-session.ts";

function state(revision = "one", expiresAt = "2026-09-15T23:00:00Z"): SourceTreeView {
  return { kind: "tree", id: "view", intent_revision: "intent", projection_revision: revision,
    state: "ready", extent: { rows: 1, complete: true }, expires_at: expiresAt,
    workspace_id: "workspace", roots: [], loading_directories: 0, intent: {} };
}

const snapshots = new Map<string, SourceTreeView>();
const presentationState = (path: string) => snapshots.get(new URL(path, "http://fixture").pathname.split("/").at(-2)!)!;
function createSourceViewsClient(json: <T>(path: string, init?: RequestInit) => Promise<T>) {
 return wireClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
  if (init?.method === "POST" && path.endsWith("/presentations")) {
   const view = await json<SourceTreeView>(path.slice(0, -14));
   const id = crypto.randomUUID(), saved = structuredClone(view);
   snapshots.set(id, saved);
   return { id, view: saved } as T;
  }
  return json<T>(path, init);
 });
}

it("detaches viewport requests without canceling or releasing the host view", async () => {
  const calls: string[] = [];
  let viewportSignal: AbortSignal | undefined;
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    calls.push(`${init?.method ?? "GET"} ${path}`);
    if (path.includes("/rows?")) {
      viewportSignal = init?.signal ?? undefined;
      return new Promise<T>((_resolve, reject) => viewportSignal?.addEventListener("abort", () => reject(new DOMException("Canceled", "AbortError")), { once: true }));
    }
    return state() as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  await session.refresh();
  const reading = session.frame(0).catch(error => error);
  await vi.waitFor(() => expect(viewportSignal).toBeDefined());
  detach();
  expect(viewportSignal?.aborted).toBe(true);
  expect((await reading).name).toBe("AbortError");
  expect(calls.some(call => call.includes("/cancel") || call.startsWith("DELETE"))).toBe(false);
  expect(session.state()?.id).toBe("view");
  await session.close();
  // Releasing the view releases its presentations with it.
  const deletes = calls.filter(call => call.startsWith("DELETE"));
  expect(deletes).toHaveLength(1);
  expect(deletes[0]).toMatch(/\/source\/views\/view$/);
});

it("keeps a view healthy while the host refuses admission and asks again", async () => {
  let refusals = 0;
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> => {
    if (!init?.method && refusals < 2) { refusals++; throw new LycaonApiError("Busy", 429, "rate_limited", { retryAfterMs: 1000 }); }
    return state() as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  try {
    await vi.waitFor(() => expect(refusals).toBe(1));
    await Promise.resolve();
    expect(session.error()).toBeUndefined();
    await vi.waitFor(() => expect(refusals).toBe(2));
    await vi.waitFor(() => expect(session.state()?.projection_revision).toBe("one"), { timeout: 3_000 });
    expect(session.error()).toBeUndefined();
  } finally { detach(); await session.close(); }
});

it("refetches host progress after returning to a detached stage", async () => {
  let current = state();
  let reads = 0;
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> => {
    if (!init?.method) reads++;
    return current as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  await session.refresh();
  detach();
  const previousReads = reads;
  current = state("two");
  receiveSourceViewEvent({ view_id: "view", kind: "tree", intent_revision: "intent", projection_revision: "two", invalidated: false, terminal: true });
  expect(reads).toBe(previousReads);
  const detachAgain = session.attach();
  await session.refresh();
  expect(session.state()?.projection_revision).toBe("two");
  detachAgain();
  await session.close();
});

it("serializes intent changes using the previously accepted revision", async () => {
  let current = state();
  const client = createSourceViewsClient(async <T>(): Promise<T> => current as T);
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const revisions: string[] = [];
  await Promise.all(["two", "three"].map(revision => session.mutate(async previous => {
    revisions.push(previous.intent_revision);
    current = { ...current, intent_revision: revision, projection_revision: revision };
    return current;
  })));
  expect(revisions).toEqual(["intent", "two"]);
  expect(session.state()?.intent_revision).toBe("three");
  await session.close();
});

it("keeps the presented coordinates while the head moves, and adopts only through an anchored follow", async () => {
  let current = state();
  const strict: string[] = [];
  const released: string[] = [];
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (init?.method === "DELETE") released.push(path);
    if (path.includes("/rows?")) {
      // A retained presentation fixes both rows and extent.
      const revision = presentationState(path).projection_revision;
      strict.push(revision);
      return { kind: "tree", view_id: "view", projection_revision: revision,
        intent_revision: "intent", extent: { rows: revision === "one" ? 1 : 2, complete: true }, span: { start: 0, end: 1 }, ancestors: [],
        anchor: { root_id: "root", path: "." }, rows: [{ address: { root_id: "root", path: "." }, name: revision, kind: "directory", depth: 0, expanded: true }] } as T;
    }
    return current as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach(); await session.refresh(); await session.frame(0);
  const published = session.presentation();
  const releasePaint = session.retainPresentation(published.id!);
  await session.refresh();
  expect(session.presentation()).toEqual(published);
  current = { ...state("two"), extent: { rows: 2, complete: true } }; await session.refresh();
  expect(session.state()?.projection_revision).toBe("two");
  expect(session.presentation().state?.projection_revision).toBe("one");
  expect(session.presentation().state?.extent.rows).toBe(1);
  await session.frame(0);
  expect(strict).toEqual(["one", "one"].slice(0, strict.length));
  expect(session.presentation().state?.projection_revision).toBe("one");
  await session.frameAt({ root_id: "root", path: "." });
  expect(session.presentation().state?.projection_revision).toBe("two");
  expect(session.presentation().state?.extent.rows).toBe(2);
  expect(session.presentation().frames[0]?.projection_revision).toBe("two");
  expect(released.some(path => path.endsWith(published.id!))).toBe(false);
  releasePaint();
  await vi.waitFor(() => expect(released.some(path => path.endsWith(published.id!))).toBe(true));
  detach(); await session.close();
});

it("reacquires an expired presentation without reopening its view", async () => {
  let expired = false;
  let creates = 0;
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (init?.method === "POST") creates++;
    if (path.includes("/rows?")) {
      if (expired) { expired = false; throw Object.assign(new Error("Expired"), { code: "source_view_not_found" }); }
      const start = Number(new URL(path, "http://fixture").searchParams.get("offset"));
      return { kind: "tree", view_id: "view", projection_revision: "one", intent_revision: "intent", extent: { rows: 400, complete: true },
        span: { start, end: start + 1 }, ancestors: [], anchor: { root_id: "root", path: "." },
        rows: [{ address: { root_id: "root", path: "." }, name: "repo", kind: "directory", depth: 0, expanded: true }] } as T;
    }
    return { ...state(), extent: { rows: 400, complete: true } } as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  try {
    await session.frame(0);
    expired = true;
    expect((await session.frame(200)).span.start).toBe(200);
    expect(creates).toBe(1);
  } finally { detach(); await session.close(); }
});

it("adopts a newer anchored frame atomically while discovery advances", async () => {
  let current = { ...state("published"), extent: { rows: 11, complete: true } };
  const client = createSourceViewsClient(async <T>(path: string): Promise<T> => {
    if (path.includes("/rows?")) {
      expect(new URL(path, "http://fixture").searchParams.has("anchor")).toBe(true);
      current = state("published");
      return { kind: "tree", view_id: "view", intent_revision: "intent", projection_revision: "published",
        extent: { rows: 11, complete: true }, span: { start: 10, end: 11 }, ancestors: [],
        anchor: { root_id: "root", path: "selected.ts" }, rows: [{ address: { root_id: "root", path: "selected.ts" }, name: "selected.ts", kind: "file", depth: 1, expanded: false }] } as T;
    }
    return current as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach(); await session.refresh();
  const frame = await session.frameAt({ root_id: "root", path: "selected.ts" });
  expect(frame.span.start).toBe(10);
  expect(session.presentation().state?.projection_revision).toBe("published");
  expect(session.presentation().frames.map(held => held.projection_revision)).toEqual(["published"]);
  detach(); await session.close();
});

it("coalesces repeated invalidations without postponing the next refresh", async () => {
  vi.useFakeTimers();
  let reads = 0;
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> => {
    if (!init?.method) reads++;
    return state() as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  try {
    await session.refresh();
    const before = reads;
    for (let i = 0; i < 10; i++) { session.invalidate(); await vi.advanceTimersByTimeAsync(20); }
    expect(reads).toBe(before);
    await vi.advanceTimersByTimeAsync(50);
    expect(reads).toBe(before + 1);
  } finally { detach(); await session.close(); vi.useRealTimers(); }
});

it("a reconnect resynchronizes active views and marks detached views for return", async () => {
  let current = state();
  let reads = 0;
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> => {
    if (!init?.method) reads++;
    return current as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach(); await session.refresh();
  current = state("two"); resyncSourceViews("project");
  await vi.waitFor(() => expect(session.state()?.projection_revision).toBe("two"));
  detach(); const previousReads = reads;
  current = state("three"); resyncSourceViews("project");
  expect(reads).toBe(previousReads);
  const again = session.attach(); await session.refresh();
  expect(session.state()?.projection_revision).toBe("three");
  again(); await session.close();
});

it("reads a preparing view again when its readiness event does not arrive", async () => {
  vi.useFakeTimers();
  let current: SourceTreeView = { ...state(), state: "preparing" };
  const client = createSourceViewsClient(async <T>(): Promise<T> => current as T);
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  try {
    const ready = session.ready();
    await vi.advanceTimersByTimeAsync(300);
    expect(session.state()?.state).toBe("preparing");
    current = state("ready");
    await vi.advanceTimersByTimeAsync(1_000);
    expect((await ready).state).toBe("ready");
  } finally { await session.close(); vi.useRealTimers(); }
});

it("preparation waiters can cancel independently without canceling the accepted view", async () => {
  let current: SourceTreeView = { ...state(), state: "preparing" };
  const calls: string[] = [];
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    calls.push(`${init?.method ?? "GET"} ${path}`);
    return current as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const abort = new AbortController();
  const first = session.ready(abort.signal).catch(error => error.name);
  const second = session.ready();
  abort.abort(); expect(await first).toBe("AbortError");
  current = state("ready"); await session.refresh();
  expect((await second).state).toBe("ready");
  expect(calls.some(call => call.includes("/cancel") || call.startsWith("DELETE"))).toBe(false);
  await session.close();
});


it("reopens an evicted view when a frame first discovers it expired", async () => {
  let current = state();
  let evicted = false;
  let creates = 0;
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (init?.method === "POST") {
      creates++;
      current = { ...state(), id: `view-${creates}` };
      evicted = false;
      return current as T;
    }
    if (init?.method === "DELETE") return undefined as T;
    if (evicted) throw Object.assign(new Error("View expired"), { code: "source_view_not_found" });
    if (path.includes("/rows?")) return { kind: "tree", view_id: current.id,
      projection_revision: current.projection_revision, intent_revision: current.intent_revision,
      extent: current.extent, span: { start: 0, end: 1 }, ancestors: [],
      anchor: { root_id: "root", path: "." }, rows: [] } as T;
    return current as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  try {
    await session.refresh();
    evicted = true;
    expect((await session.frame(0)).view_id).toBe("view-2");
    expect(creates).toBe(2);
    expect(session.error()).toBeUndefined();
  } finally { detach(); await session.close(); }
});

it("keeps a pending replacement acquisition when an outgoing page finishes", async () => {
  let current = { ...state("one"), extent: { rows: 400, complete: true } };
  let releaseReplacement!: () => void;
  const waiting = new Promise<void>(resolve => { releaseReplacement = resolve; });
  let acquiring = false;
  const releases: string[] = [];
  const retained = new Map<string, SourceTreeView>();
  const client = wireClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (path.endsWith("/presentations") && init?.method === "POST") {
      const view = structuredClone(current), id = view.projection_revision;
      retained.set(id, view);
      if (id === "two") { acquiring = true; await waiting; }
      return { id, view } as T;
    }
    if (init?.method === "DELETE") { releases.push(path); return undefined as T; }
    if (path.includes("/rows?")) {
      const url = new URL(path, "http://fixture");
      const id = url.pathname.split("/presentations/")[1]!.split("/")[0]!;
      const view = retained.get(id)!;
      const start = Number(url.searchParams.get("offset") ?? 0);
      return { kind: "tree", view_id: view.id, intent_revision: view.intent_revision,
        projection_revision: view.projection_revision, extent: view.extent,
        span: { start, end: start + 1 }, ancestors: [], anchor: { root_id: "root", path: "." },
        rows: [{ address: { root_id: "root", path: "." }, name: id, kind: "directory", depth: 0, expanded: true }] } as T;
    }
    return current as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  try {
    await session.frame(0);
    current = { ...current, projection_revision: "two" };
    await session.refresh();
    const replacement = session.frameAt({ root_id: "root", path: "." });
    await vi.waitFor(() => expect(acquiring).toBe(true));
    await session.frame(200);
    expect(session.presentation().state?.projection_revision).toBe("one");
    expect(releases).toEqual([]);
    releaseReplacement();
    expect((await replacement).projection_revision).toBe("two");
    expect(session.presentation().state?.projection_revision).toBe("two");
    await vi.waitFor(() => expect(releases).toEqual(["/v1/projects/project/source/views/view/presentations/one"]));
  } finally { releaseReplacement(); detach(); await session.close(); }
});

it("cancels an acquisition waiter without cancelling the shared acquisition", async () => {
  let release!: (value: { id: string; view: SourceTreeView }) => void;
  const acquisition = new Promise<{ id: string; view: SourceTreeView }>(resolve => { release = resolve; });
  const base = createSourceViewsClient(async <T>(): Promise<T> => state() as T);
  const acquire = vi.fn(() => acquisition);
  const client = { ...base, createSourcePresentation: acquire, getSourceViewRows: vi.fn(async () => ({
    kind: "tree" as const, view_id: "view", intent_revision: "intent", projection_revision: "one",
    extent: { rows: 1, complete: true }, span: { start: 0, end: 1 }, ancestors: [],
    anchor: { root_id: "root", path: "." }, rows: [],
  })) };
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  try {
    await session.ready();
    const abort = new AbortController();
    const first = session.frameAt({ root_id: "root", path: "." }, abort.signal);
    const cancelled = expect(first).rejects.toMatchObject({ name: "AbortError" });
    const second = session.frameAt({ root_id: "root", path: "." });
    await vi.waitFor(() => expect(acquire).toHaveBeenCalledTimes(1));
    abort.abort(); await cancelled;
    expect(client.getSourceViewRows).not.toHaveBeenCalled();
    release({ id: "presentation", view: state() });
    await second;
    expect(acquire).toHaveBeenCalledTimes(1);
    expect(client.getSourceViewRows).toHaveBeenCalledTimes(1);
  } finally { release({ id: "presentation", view: state() }); detach(); await session.close(); }
});

it.each(["expired", "failed", "failed after refresh"] as const)("applies collapse after replacing a %s view without waiting for coverage or cleanup", async outcome => {
  const preparing: SourceTreeView = { ...state(), id: "replacement", state: "preparing", extent: { rows: 0, complete: false } };
  let creates = 0;
  let finishCleanup!: () => void;
  const cleanup = new Promise<void>(resolve => { finishCleanup = resolve; });
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (init?.method === "POST") creates++;
    if (init?.method === "DELETE" && path.endsWith("/view")) await cleanup;
    return (creates > 1 ? preparing : { ...state(), state: outcome === "failed" || outcome === "failed after refresh" && !init?.method ? "failed" : "ready" }) as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const attempts: string[] = [];
  try {
    await session.open();
    const result = await session.mutate(async previous => {
      attempts.push(previous.id);
      if (previous.id === "view") throw Object.assign(new Error("Unavailable"), { code: outcome === "expired" ? "source_view_not_found" : "source_view_preparing" });
      return { ...previous, state: "ready", extent: { rows: 1, complete: true } };
    }, { restartFailed: true });
    expect(attempts).toEqual(outcome === "failed" ? ["replacement"] : ["view", "replacement"]);
    expect(result.state).toBe("ready");
  } finally { finishCleanup(); await session.close(); }
});

it("retries command admission during initialization without requiring complete coverage", async () => {
  vi.useFakeTimers();
  const current: SourceTreeView = { ...state(), state: "preparing", extent: { rows: 0, complete: false } };
  const client = createSourceViewsClient(async <T>(): Promise<T> => current as T);
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const apply = vi.fn(async (previous: SourceTreeView): Promise<SourceTreeView> => {
    if (apply.mock.calls.length === 1) throw Object.assign(new Error("Initializing"), { code: "source_view_preparing" });
    return { ...previous, state: "ready" };
  });
  try {
    const result = session.mutate(apply);
    await vi.advanceTimersByTimeAsync(249);
    expect(apply).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect((await result).state).toBe("ready");
    expect(apply).toHaveBeenCalledTimes(2);
  } finally { await session.close(); vi.useRealTimers(); }
});

it("cancels a readiness waiter while the shared view is still being created", async () => {
  let release!: (value: SourceTreeView) => void;
  const creating = new Promise<SourceTreeView>(resolve => { release = resolve; });
  const base = createSourceViewsClient(async <T>(): Promise<T> => state() as T);
  const client = { ...base, createSourceView: vi.fn(() => creating) };
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  try {
    const abort = new AbortController();
    const first = session.ready(abort.signal);
    const cancelled = expect(first).rejects.toMatchObject({ name: "AbortError" });
    const second = session.ready();
    abort.abort(); await cancelled;
    release(state());
    expect((await second).state).toBe("ready");
    expect(client.createSourceView).toHaveBeenCalledTimes(1);
  } finally { release(state()); await session.close(); }
});

it("replaces an expired view on source_workspace_mismatch during refresh and does not retry in tight loops", async () => {
  let creates = 0;
  let _getCalls = 0;
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> => {
    if (init?.method === "POST") {
      creates++;
      return { ...state(), id: `view-${creates}` } as T;
    }
    _getCalls++;
    if (creates === 1) {
      throw new LycaonApiError("Workspace mismatch", 409, "source_workspace_mismatch");
    }
    return { ...state(), id: `view-${creates}` } as T;
  });
  const session = new SourceViewSession(
    client,
    "project",
    { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} },
    frame => ({ rows: frame.rows.length, bytes: 256 }),
  );
  const detach = session.attach();
  try {
    const refreshed = await session.refresh();
    expect(creates).toBe(2);
    expect(refreshed.id).toBe("view-2");
    expect(session.state()?.id).toBe("view-2");
    expect(session.error()).toBeUndefined();
  } finally {
    detach();
    await session.close();
  }
});


const treeRequest = (sessionId?: string) => ({ kind: "tree" as const, client_id: "window:main", operation_id: "create", workspace_id: "workspace",
  ...(sessionId ? { session_id: sessionId } : {}), intent: {} });

it("reopens an expired view with the same intent and never reads not_found as expiry", async () => {
  let creates = 0;
  let expired = true;
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (path.endsWith("/apply")) throw new LycaonApiError("File edit not found.", 404, "not_found");
    if (init?.method === "POST") { creates++; return { ...state(), id: `view-${creates}` } as T; }
    if (expired && creates === 1) { expired = false; throw new LycaonApiError("Expired", 404, "source_view_not_found"); }
    return { ...state(), id: `view-${creates}` } as T;
  });
  const session = new SourceViewSession(client, "project", treeRequest(), frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  try {
    await vi.waitFor(() => expect(session.state()?.id).toBe("view-2"));
    expect(session.error()).toBeUndefined();
    const failed = await session.mutate(async previous => client.applySourceViewIntent("project", previous.id,
      { kind: "tree", operation_id: "reveal", expected_intent_revision: previous.intent_revision, command: { kind: "reveal", address: { root_id: "r", path: "gone.ts" } } })).catch(error => error);
    expect(failed).toMatchObject({ code: "not_found" });
    expect(creates).toBe(2);
  } finally { detach(); await session.close(); }
});

it("ends with its chat: session_not_found closes the view as superseded work, not a failure", async () => {
  const calls: string[] = [];
  let chatDeleted = false;
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    calls.push(`${init?.method ?? "GET"} ${path}`);
    if (init?.method === "DELETE") return undefined as T;
    if (chatDeleted) throw new LycaonApiError("This chat no longer exists.", 404, "session_not_found");
    return state() as T;
  });
  const session = new SourceViewSession(client, "project", treeRequest("chat-1"), frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  await vi.waitFor(() => expect(session.state()?.id).toBe("view"));
  chatDeleted = true;
  const refreshed = await session.refresh().catch(error => error);
  expect(refreshed.name).toBe("AbortError");
  expect(session.isReleased()).toBe(true);
  await vi.waitFor(() => expect(calls.filter(call => call.startsWith("POST"))).toHaveLength(1));
  expect(calls.some(call => call.startsWith("DELETE") && call.endsWith("/source/views/view"))).toBe(true);
  const reading = await session.ready().catch(error => error);
  expect(reading.name).toBe("AbortError");
  detach();
});

it("a deleted chat ends only the views it addressed", async () => {
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> =>
    (init?.method === "DELETE" ? undefined : state()) as T);
  const addressed = new SourceViewSession(client, "project", treeRequest("chat-1"), frame => ({ rows: frame.rows.length, bytes: 256 }));
  const other = new SourceViewSession(client, "project", treeRequest("chat-2"), frame => ({ rows: frame.rows.length, bytes: 256 }));
  const unaddressed = new SourceViewSession(client, "project", treeRequest(), frame => ({ rows: frame.rows.length, bytes: 256 }));
  await Promise.all([addressed.open(), other.open(), unaddressed.open()]);
  endSourceViewsForChat("chat-1");
  await vi.waitFor(() => expect(addressed.isReleased()).toBe(true));
  expect(other.isReleased()).toBe(false);
  expect(unaddressed.isReleased()).toBe(false);
  endSourceViewsForChat("chat-1");
  await Promise.all([other.close(), unaddressed.close()]);
});

it("a readiness wait on a detached view makes no request while the host lease holds", async () => {
  const held = new Date(Date.now() + 10 * 60_000).toISOString();
  let reads = 0;
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> => {
    if (!init?.method) reads++;
    return state("one", held) as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  // The first attach validates the opened handle with one read.
  await vi.waitFor(() => expect(reads).toBe(1));
  await vi.waitFor(() => expect(session.state()?.projection_revision).toBe("one"));
  detach();
  await session.ready();
  await session.ready();
  const again = session.attach();
  await session.open();
  await new Promise(resolve => setTimeout(resolve, 20));
  expect(reads).toBe(1);
  again();
  await session.close();
});

it("attach reads a detached view again once its host lease has lapsed", async () => {
  const lapsed = new Date(Date.now() + 30_000).toISOString();
  let reads = 0;
  const client = createSourceViewsClient(async <T>(_path: string, init?: RequestInit): Promise<T> => {
    if (!init?.method) reads++;
    return state("one", lapsed) as T;
  });
  const session = new SourceViewSession(client, "project", { kind: "tree", client_id: "window:main", operation_id: "create", workspace_id: "workspace", intent: {} }, frame => ({ rows: frame.rows.length, bytes: 256 }));
  const detach = session.attach();
  await vi.waitFor(() => expect(reads).toBe(1));
  await vi.waitFor(() => expect(session.state()?.projection_revision).toBe("one"));
  detach();
  const again = session.attach();
  await vi.waitFor(() => expect(reads).toBe(2));
  again();
  await session.close();
});
