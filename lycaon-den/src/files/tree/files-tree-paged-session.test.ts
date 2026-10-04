import { expect, it, vi } from "vitest";
import { createSourceViewsClient } from "../../api/source-views-client.ts";
import type { SourceTreeCommand, SourceTreeFrame, SourceTreeView, SourceTreeViewCreate } from "../../api/types.ts";
import { saveFilesTreeIntent } from "./files-tree-intent-state.ts";
import { FilesTreeSession } from "./files-tree-paged-session.ts";
import { buildPagedTreeModel, UNKNOWN_SUBTREE_END } from "./files-tree-paged-model.ts";
import { treeLayoutRange } from "./files-tree-sticky.ts";
import { TREE_ROOTS, treeRow, treeViewFixture } from "../../test/source-tree-view-fixture.ts";

vi.mock("./files-tree-intent-state.ts", () => ({ saveFilesTreeIntent: vi.fn(async () => {}) }));

it.each([200, 399])("prepares fold context across a cold page boundary at row %i", async start => {
  const host = treeViewFixture();
  host.large(1_000, index => treeRow(index ? `file-${index}.ts` : ".", index ? "file" : "directory", !index));
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.range(start, start + 1, new AbortController().signal);
    const model = buildPagedTreeModel(TREE_ROOTS, 1_000, session.presentation().frames);
    const context = treeLayoutRange(start, start + 1, model.length);
    for (let index = context.start; index < context.end; index++) expect(model.rowAt(index), `layout row ${index}`).toBeDefined();
  } finally { detach(); await session.close(); }
});

it("waits for coalesced revalidation instead of retrying stale row reads in a loop", async () => {
  vi.useFakeTimers();
  const host = treeViewFixture();
  let attempts = 0;
  host.read(async () => {
    attempts++;
    if (attempts === 1) throw Object.assign(new Error("Changed projection"), { code: "source_view_revision_changed" });
  });
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.refresh();
    const reading = session.range(0, 2, new AbortController().signal);
    await vi.advanceTimersByTimeAsync(200);
    expect(attempts).toBe(1);
    host.notify();
    await vi.advanceTimersByTimeAsync(50);
    await reading;
    expect(attempts).toBe(2);
    expect(session.frames.frames()[0]?.rows).toHaveLength(2);
  } finally { detach(); await session.close(); vi.useRealTimers(); }
});

it.each(["expand", "folder"])("recovers an expired view for %s commands", async operation => {
  let expired = false;
  const creates: SourceTreeViewCreate[] = [];
  const expansions: { path: string; body: { base_presentation_id?: string } }[] = [];
  let current: SourceTreeView;
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (path.endsWith("/views") && init?.method === "POST") {
      const request = JSON.parse(String(init.body)) as SourceTreeViewCreate;
      creates.push(request);
      current = { kind: "tree", id: `view-${creates.length}`, workspace_id: "workspace", roots: [], intent: request.intent,
        intent_revision: "intent", projection_revision: "projection", state: "ready",
        extent: { rows: 0, complete: true }, loading_directories: 0, expires_at: "2026-09-16T23:00:00Z" };
    }
    if (expired && path.includes("/views/view-1")) throw Object.assign(new Error("Expired view"), { code: "source_view_not_found" });
    if (init?.method === "POST" && path.endsWith("/apply")) expansions.push({ path, body: JSON.parse(String(init.body)) });
    return current as T;
  });
  const intent = { filter: "src" };
  const session = new FilesTreeSession(client, "project", "workspace", undefined, intent);
  await session.open();
  expired = true;
  try {
    if (operation === "expand") await session.expand([{ root_id: "root", path: "." }], "old-presentation");
    else await session.command({ kind: "disclose", disclosures: [{ address: { root_id: "root", path: "src" }, open: true, recursive: false }] }, "old-presentation");
    expect(creates).toHaveLength(2);
    expect(creates[1]?.intent).toEqual(intent);
    expect(expansions.map(value => value.path)).toEqual(["/v1/projects/project/source/views/view-2/apply"]);
    expect(expansions[0]?.body.base_presentation_id).toBeUndefined();
    expect(session.error()).toBeUndefined();
  } finally { await session.close(); }
});

function scriptedHost(options: { command?: (path: string) => void } = {}) {
  const views = new Map<string, SourceTreeView>();
  const patches: { path: string; body: { base_presentation_id?: string } }[] = [];
  let expired = "";
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    const view = (id: string) => views.get(id)!;
    if (init?.method === "POST" && path.endsWith("/views")) {
      const request = JSON.parse(String(init.body)) as SourceTreeViewCreate;
      const created: SourceTreeView = { kind: "tree", id: `view-${views.size + 1}`, workspace_id: "workspace", roots: [], intent: request.intent,
        intent_revision: "intent", projection_revision: "projection", state: "ready", extent: { rows: 0, complete: true },
        loading_directories: 0, expires_at: "2026-09-16T23:00:00Z" };
      views.set(created.id, created);
      return created as T;
    }
    const id = /\/views\/(view-\d+)/.exec(path)?.[1] ?? "";
    if (init?.method === "POST" && path.endsWith("/presentations")) return { id: `presentation-of-${id}`, view: view(id) } as T;
    if (init?.method === "POST" && path.endsWith("/apply")) {
      patches.push({ path, body: JSON.parse(String(init.body)) });
      options.command?.(path);
      return view(id) as T;
    }
    if (id === expired) throw Object.assign(new Error("Expired view"), { code: "source_view_not_found" });
    if (path.includes("/rows?")) {
      const held = view(id);
      return { kind: "tree", view_id: id, intent_revision: held.intent_revision, projection_revision: held.projection_revision,
        extent: held.extent, span: { start: 0, end: 0 }, anchor: { root_id: "root", path: "." }, rows: [], ancestors: [] } as T;
    }
    return view(id) as T;
  });
  return { client, patches, expire: (id: string) => { expired = id; }, creates: () => views.size };
}

it("reads a replacement view even where the expired view's rows are still painted", async () => {
  const host = treeViewFixture();
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.range(0, 2, new AbortController().signal);
    const expired = session.state()!.id;
    host.rows([{ ...treeRow(".", "directory", true), name: "renamed" }, treeRow("README.md")]);
    host.expire();
    await session.refresh();
    expect(session.state()!.id).not.toBe(expired);
    expect(session.presentation().state?.id).toBe(expired);
    expect(await session.prefetch(0, new AbortController().signal)).toBe(true);
    expect(session.frames.frames().find(frame => frame.view_id === session.state()!.id)?.rows[0]?.name).toBe("renamed");
  } finally { detach(); await session.close(); }
});

it("keeps its view when a command names something absent", async () => {
  const host = scriptedHost({ command: () => { throw Object.assign(new Error("Not found"), { code: "not_found" }); } });
  const session = new FilesTreeSession(host.client, "project", "workspace", undefined, {});
  try {
    await session.open();
    await expect(session.command({ kind: "disclose", disclosures: [{ address: { root_id: "root", path: "gone" }, open: true, recursive: false }] }))
      .rejects.toMatchObject({ code: "not_found" });
    expect(host.creates()).toBe(1);
    expect(host.patches.map(patch => patch.path)).toEqual(["/v1/projects/project/source/views/view-1/apply"]);
  } finally { await session.close(); }
});

it("sends a displayed basis only to the view that presented it, and only once", async () => {
  const host = scriptedHost();
  const session = new FilesTreeSession(host.client, "project", "workspace", undefined, {});
  const disclose: SourceTreeCommand = { kind: "disclose", disclosures: [{ address: { root_id: "root", path: "src" }, open: true, recursive: false }] };
  const detach = session.attach();
  try {
    await session.frame(0);
    const painted = session.presentation().id;
    expect(painted).toBe("presentation-of-view-1");
    await session.command(disclose, painted);
    expect(host.patches.at(-1)?.body.base_presentation_id).toBe(painted);
    // view-2 has presented nothing, so view-1's painted presentation is still current.
    host.expire("view-1");
    await session.refresh();
    expect(session.state()?.id).toBe("view-2");
    expect(session.presentation().id).toBe(painted);
    await session.command(disclose, painted);
    expect(host.patches.at(-1)?.path).toBe("/v1/projects/project/source/views/view-2/apply");
    expect(host.patches.at(-1)?.body.base_presentation_id).toBeUndefined();
  } finally { detach(); await session.close(); }
});

it("uses a strict basis for the first page of an offset range", async () => {
  const host = treeViewFixture();
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.range(0, 2, new AbortController().signal);
    const request = host.requests.find(request => new URL(request.path, "http://fixture").pathname.endsWith("/rows"));
    const query = new URL(request!.path, "http://fixture").searchParams;
    expect(new URL(request!.path, "http://fixture").pathname).toMatch(/\/presentations\/[^/]+\/rows$/);
    expect(query.get("offset")).toBe("0");
    expect(query.has("follow")).toBe(false);
    expect(query.has("anchor")).toBe(false);
  } finally { detach(); await session.close(); }
});

it("waits for a replacement review projection when preparation races a frame", async () => {
  const host = treeViewFixture();
  let preparing = false, first = true;
  host.read(async () => {
    if (!first) return;
    first = false; preparing = true;
    throw Object.assign(new Error("Preparing review"), { code: "source_view_preparing" });
  });
  const client = { ...host.client, getSourceView: async (...args: Parameters<typeof host.client.getSourceView>) => {
    const state = await host.client.getSourceView(...args);
    return preparing ? { ...state, state: "preparing" as const, extent: { rows: 0, complete: false } } : state;
  } };
  const session = new FilesTreeSession(client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    let settled = false;
    const reading = session.range(0, 2, new AbortController().signal).then(() => { settled = true; });
    await expect.poll(() => session.state()?.state).toBe("preparing");
    expect(settled).toBe(false);
    preparing = false; host.notify();
    await reading;
    expect(session.frames.frames()[0]?.rows).toHaveLength(2);
  } finally { detach(); await session.close(); }
});

it("keeps a strict read in its declared coordinates while the head publishes behind it", async () => {
  const host = treeViewFixture();
  let first = true;
  let declared: string | null = null;
  host.read(async signal => {
    if (!first) return;
    first = false;
    declared = host.state().projection_revision;
    host.notify();
    await new Promise(resolve => setTimeout(resolve, 20));
    expect(signal?.aborted).toBe(false);
  });
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    const interest = new AbortController();
    await session.range(0, 2, interest.signal);
    expect(interest.signal.aborted).toBe(false);
    expect(host.requests.filter(request => request.path.includes("/rows?"))).toHaveLength(1);
    expect(session.frames.frames()[0]?.projection_revision).toBe(declared);
    expect(session.frames.frames()[0]?.rows).toHaveLength(2);
    await expect.poll(() => session.state()?.projection_revision).toBe(host.state().projection_revision);
    expect(session.presentation().state?.projection_revision).toBe(declared);
  } finally { detach(); await session.close(); }
});

it.each([false, true])("recovers failed preparation and preserves recursive disclosure (explicit expansion=%s)", async explicit => {
  const intent = { disclosures: [{ address: { root_id: "root", path: "." }, open: true, recursive: true }], filter: "test" };
  const creates: SourceTreeViewCreate[] = [];
  const released: string[] = [];
  const expanded: unknown[] = [];
  let current: SourceTreeView;
  const client = createSourceViewsClient(async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (path.endsWith("/apply")) { expanded.push(JSON.parse(String(init?.body))); return current as T; }
    if (init?.method === "POST") {
      const request = JSON.parse(String(init.body)) as SourceTreeViewCreate;
      creates.push(request);
      current = { kind: "tree", id: `view-${creates.length}`, workspace_id: "workspace", roots: [], intent: request.intent,
        intent_revision: "intent", projection_revision: "projection", state: creates.length === 1 ? "failed" : "ready",
        failure: creates.length === 1 ? { code: "unavailable", message: "Saved expansion is unavailable." } : undefined,
        extent: { rows: 0, complete: true }, loading_directories: 0, expires_at: "2026-09-16T23:00:00Z" };
    }
    if (init?.method === "DELETE") released.push(path);
    return current as T;
  });
  const session = new FilesTreeSession(client, "project", "workspace", undefined, intent);
  const detach = session.attach();
  await expect(session.ready()).rejects.toThrow("Saved expansion");
  expect(creates).toHaveLength(1);
  if (explicit) await session.expand([{ root_id: "root", path: "selected" }]);
  else expect((await session.retryPreparation()).state).toBe("ready");
  expect(creates[1]?.operation_id).not.toBe(creates[0]?.operation_id);
  expect(creates[1]?.intent).toEqual(intent);
  expect(expanded).toHaveLength(explicit ? 1 : 0);
  if (explicit) expect(expanded[0]).toMatchObject({ command: { kind: "disclose", disclosures: [{ address: { root_id: "root", path: "selected" }, open: true, recursive: true }] } });
  expect(released).toEqual(["/v1/projects/project/source/views/view-1"]);
  detach(); await session.close();
});


it("abandons rejected read-ahead without refreshing or retrying", async () => {
  vi.useFakeTimers();
  const host = treeViewFixture();
  const read = vi.fn(async () => { throw Object.assign(new Error("Changed projection"), { code: "source_view_revision_changed" }); });
  host.read(read);
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.refresh();
    await vi.advanceTimersByTimeAsync(1_000);
    const before = host.requests.length;
    expect(await session.prefetch(0, new AbortController().signal)).toBe(false);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(read).toHaveBeenCalledTimes(1);
    expect(host.requests.slice(before).filter(request => request.path.includes("/rows?"))).toHaveLength(1);
    expect(session.error()).toBeUndefined();
  } finally { detach(); await session.close(); vi.useRealTimers(); }
});


it.each([true, false])("retains cached positions only with host validation (%s)", async accept => {
  const host = treeViewFixture();
  host.large(1000, index => treeRow(index ? `file-${index}.ts` : ".", index ? "file" : "directory", !index));
  const client = { ...host.client, getSourceViewRows: async (...args: Parameters<typeof host.client.getSourceViewRows>) => {
    const frame = await host.client.getSourceViewRows(...args) as SourceTreeFrame;
    return { ...frame, prefix: { end: frame.span.end, fingerprint: "a".repeat(64) },
      retained_prefix: accept ? args[2].retain?.[0] : undefined };
  } };
  const session = new FilesTreeSession(client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.frameAt({ root_id: "r1", path: "." });
    await session.range(200, 600, new AbortController().signal);
    const old = session.presentation().frames.find(frame => frame.span.start === 400)!;
    host.notify(); await session.refresh();
    await session.frameAt({ root_id: "r1", path: "." });
    const held = session.frames.frames().find(frame => frame.span.start === 400);
    if (accept) {
      expect(held?.rows).toBe(old.rows);
      expect(held?.projection_revision).toBe(host.state().projection_revision);
      // The root's subtree reaches past the proven prefix, so its end is unknown.
      expect(held?.ancestors[0]?.end).toBe(UNKNOWN_SUBTREE_END);
    } else expect(held).toBeUndefined();
  } finally { detach(); await session.close(); }
});

it("read-ahead fills the presented coordinates while discovery publishes a larger head", async () => {
  const host = treeViewFixture();
  const rowAt = (index: number) => treeRow(index ? `file-${index}.ts` : ".", index ? "file" : "directory", !index);
  host.large(300, rowAt);
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    const first = await session.frameAt({ root_id: "r1", path: "." });
    host.large(1000, rowAt);
    host.notify();
    await session.refresh();
    expect(session.state()?.extent.rows).toBe(1000);
    expect(await session.prefetch(200, new AbortController().signal)).toBe(true);
    const page = session.presentation().frames.find(frame => frame.span.start === 200);
    expect(page?.projection_revision).toBe(first.projection_revision);
    expect(page?.extent.rows).toBe(300);
    expect(page?.span.end).toBe(300);
    expect(session.presentation().state?.projection_revision).toBe(first.projection_revision);
    expect(session.presentation().state?.extent.rows).toBe(300);
    const request = host.requests.filter(request => request.path.includes("/rows?")).at(-1)!;
    expect(request.path.split("/presentations/")[1]?.split("/")[0]).toBe(host.requests.find(value => value.path.includes("/rows?"))!.path.split("/presentations/")[1]?.split("/")[0]);
  } finally { detach(); await session.close(); }
});

it("follows the head only through an anchored read, which declares the presented basis", async () => {
  const host = treeViewFixture();
  const rowAt = (index: number) => treeRow(index ? `file-${index}.ts` : ".", index ? "file" : "directory", !index);
  host.large(300, rowAt);
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    const first = await session.frameAt({ root_id: "r1", path: "." });
    host.large(1000, rowAt);
    host.notify();
    await session.refresh();
    const adopted = await session.frameAt({ root_id: "r1", path: "file-1.ts" });
    const request = host.requests.filter(request => request.path.includes("/rows?")).at(-1)!;
    const query = new URL(request.path, "http://fixture").searchParams;
    expect(query.has("anchor")).toBe(true);
    expect(adopted.projection_revision).not.toBe(first.projection_revision);
    expect(adopted.projection_revision).toBe(host.state().projection_revision);
    expect(session.presentation().state?.extent.rows).toBe(1000);
  } finally { detach(); await session.close(); }
});

it("locates in the presented coordinates and reads the row strictly there", async () => {
  const host = treeViewFixture();
  const rowAt = (index: number) => treeRow(index ? `file-${index}.ts` : ".", index ? "file" : "directory", !index);
  host.large(300, rowAt);
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    const first = await session.frameAt({ root_id: "r1", path: "." });
    host.large(1000, rowAt);
    host.notify();
    await session.refresh();
    expect(await session.locate({ root_id: "r1", path: "file-250.ts" })).toBe(250);
    const locate = host.requests.find(request => request.path.includes("/locate?"))!;
    expect(locate.path.split("/presentations/")[1]?.split("/")[0]).toBe(host.requests.find(value => value.path.includes("/rows?"))!.path.split("/presentations/")[1]?.split("/")[0]);
    expect(session.presentation().state?.projection_revision).toBe(first.projection_revision);
  } finally { detach(); await session.close(); }
});


it.each(["locate", "anchor"])("resumes %s when review preparation races navigation", async operation => {
  vi.useFakeTimers();
  const host = treeViewFixture();
  let preparing = false;
  const conflict = () => Object.assign(new Error("Preparing review"), { code: "source_view_preparing" });
  const locate = vi.fn(async (...args: Parameters<typeof host.client.locateSourceView>) => {
    if (!preparing) { preparing = true; throw conflict(); }
    return host.client.locateSourceView(...args);
  });
  let firstFrame = true;
  host.read(async () => {
    if (operation === "anchor" && firstFrame) { firstFrame = false; preparing = true; throw conflict(); }
  });
  const client = { ...host.client, locateSourceView: locate };
  const session = new FilesTreeSession(client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.refresh();
    const reading = operation === "locate" ? session.locate({ root_id: "r1", path: "README.md" }) : session.frameAt({ root_id: "r1", path: "." });
    await vi.advanceTimersByTimeAsync(200);
    expect(preparing).toBe(true);
    expect(session.error()).toBeUndefined();
    await vi.advanceTimersByTimeAsync(50);
    const result = await reading;
    if (operation === "locate") expect(result).toBe(1);
    else expect(result).toMatchObject({ rows: expect.any(Array) });
    expect(session.frames.frames()[0]?.rows).toHaveLength(2);
  } finally { detach(); await session.close(); vi.useRealTimers(); }
});

it("bounds retries and cancels a navigation whose host remains preparing", async () => {
  vi.useFakeTimers();
  const host = treeViewFixture();
  const locate = vi.fn(async () => { throw Object.assign(new Error("Preparing review"), { code: "source_view_preparing" }); });
  const session = new FilesTreeSession({ ...host.client, locateSourceView: locate }, "project", "ws1", undefined, {});
  const detach = session.attach();
  const abort = new AbortController();
  try {
    await session.refresh();
    const reading = session.locate({ root_id: "r1", path: "README.md" }, abort.signal);
    const rejected = expect(reading).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(1_000);
    expect(locate.mock.calls.length).toBeLessThanOrEqual(5);
    abort.abort();
    await rejected;
    const attempts = locate.mock.calls.length;
    await vi.advanceTimersByTimeAsync(2_000);
    expect(locate).toHaveBeenCalledTimes(attempts);
    expect(session.error()).toBeUndefined();
  } finally { detach(); await session.close(); vi.useRealTimers(); }
});


it("publishes accepted disclosure only after its immutable configuration is saved", async () => {
  const host = treeViewFixture();
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  vi.mocked(saveFilesTreeIntent).mockImplementationOnce(async () => gate);
  try {
    const before = await session.ready();
    const command = session.command({ kind: "disclose", disclosures: [{ address: { root_id: "r1", path: "." }, open: true, recursive: true }] });
    await expect.poll(() => host.commands.length).toBe(1);
    await session.refresh();
    expect(session.state()?.intent_revision).toBe(before.intent_revision);
    let ready = false;
    const reader = session.ready().then(() => { ready = true; });
    await Promise.resolve();
    expect(ready).toBe(false);
    release();
    await command; await reader;
    expect(session.state()?.intent_revision).toBe(host.state().intent_revision);
  } finally { release(); detach(); await session.close(); }
});

it("dispatches folder toggles while accepted expansion prepares and keeps the complete presentation", async () => {
  const rows = [treeRow(".", "directory", true), treeRow("src", "directory"), treeRow("README.md")];
  const host = treeViewFixture(rows);
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.range(0, rows.length, new AbortController().signal);
    const painted = session.presentation();
    expect(painted.id).toBeDefined();
    expect(painted.state?.extent.complete).toBe(true);
    host.update(() => { host.preparing(true); });

    await session.expand([{ root_id: "r1", path: "." }], painted.id);
    const expanded = host.state();
    expect(expanded.state).toBe("preparing");
    expect(session.state()?.intent_revision).toBe(expanded.intent_revision);

    await session.command({ kind: "toggle", address: { root_id: "r1", path: "src" } }, painted.id);

    expect(host.commands.map(command => command.kind)).toEqual(["disclose", "toggle"]);
    const patches = host.requests.filter(request => request.path.endsWith("/apply"));
    expect(patches[1]?.body).toMatchObject({
      expected_intent_revision: expanded.intent_revision,
      base_presentation_id: painted.id,
      command: { kind: "toggle", address: { root_id: "r1", path: "src" } },
    });
    expect(host.state().state).toBe("preparing");
    expect(session.state()?.intent_revision).toBe(host.state().intent_revision);
    expect(session.presentation()).toEqual(painted);
    const retained = await session.frame(0, rows.length);
    expect(retained.rows).toEqual(rows);
    expect(retained.extent.complete).toBe(true);
    expect(host.requests.filter(request => request.method === "POST" && request.path.endsWith("/presentations"))).toHaveLength(1);
    expect(session.activity()).toMatchObject({ queuedCommands: 0, commandPhase: "idle", pendingAcceptance: undefined });
    expect(session.error()).toBeUndefined();
  } finally { detach(); await session.close(); }
});

it("keeps an unsaved acceptance unpublished and saves it before the next command", async () => {
  const host = treeViewFixture();
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  vi.mocked(saveFilesTreeIntent).mockRejectedValueOnce(new Error("Disk unavailable"));
  try {
    const before = await session.ready();
    const command = { kind: "disclose" as const, disclosures: [{ address: { root_id: "r1", path: "." }, open: true, recursive: true }] };
    await expect(session.command(command)).rejects.toThrow("Disk unavailable");
    await session.refresh();
    expect(session.state()?.intent_revision).toBe(before.intent_revision);
    const accepted = host.state().intent_revision;
    await session.command(command);
    expect(saveFilesTreeIntent).toHaveBeenCalledWith("ws1", accepted, expect.any(Array));
    expect(session.state()?.intent_revision).toBe(host.state().intent_revision);
  } finally { detach(); await session.close(); }
});


it.each([
  { at: 0, before: 0, offset: 0, start: 0 },
  { at: 10, before: 0, offset: 0, start: 10 },
  { at: 530, before: 30, offset: 0, start: 500 },
  { at: 0, before: 0, offset: 5_000, start: 5_000 },
  { at: 0, before: 39, offset: Number.MAX_SAFE_INTEGER, start: 9_960 },
])("buffers either side of an anchored viewport at $start", async ({ at, before, offset, start }) => {
  const host = treeViewFixture();
  host.rows(Array.from({ length: 10_000 }, (_, index) => treeRow(index === 0 ? "." : `file-${index}`)));
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    const result = await session.viewport({ address: { root_id: "r1", path: at === 0 ? "." : `file-${at}` }, before, offset }, 40, new AbortController().signal);
    expect(result.start).toBe(start);
    expect(result.frame.span.start).toBeLessThanOrEqual(Math.max(0, start - 80));
    expect(result.frame.span.end).toBeGreaterThanOrEqual(Math.min(10_000, start + 40));
  } finally { detach(); await session.close(); }
});


it("does not restart an anchored read after its presenter has detached", async () => {
  const host = treeViewFixture();
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  try {
    await expect(session.frameAt({ root_id: "r1", path: "." })).rejects.toMatchObject({ name: "AbortError" });
    await expect(session.range(0, 1, new AbortController().signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(host.requests.filter(request => request.path.includes("/rows?"))).toHaveLength(0);
    const detach = session.attach();
    try { expect((await session.frameAt({ root_id: "r1", path: "." })).rows).toHaveLength(2); }
    finally { detach(); }
  } finally { await session.close(); }
});

it("orders expand before a later collapse even while a refresh is stalled", async () => {
  const host = treeViewFixture();
  let release!: () => void;
  const delayed = new Promise<void>(resolve => { release = resolve; });
  const client = { ...host.client, getSourceView: async (...args: Parameters<typeof host.client.getSourceView>) => {
    await delayed; return host.client.getSourceView(...args);
  } };
  const session = new FilesTreeSession(client, "project", "ws1", undefined, {});
  try {
    await session.open();
    const expanding = session.expand([{ root_id: "r1", path: "." }]);
    const collapsing = session.command({ kind: "disclose", disclosures: [{ address: { root_id: "r1", path: "." }, open: false, recursive: true }] });
    await Promise.all([expanding, collapsing]);
    expect(host.commands.map(command => command.kind === "disclose" && command.disclosures[0]?.open)).toEqual([true, false]);
  } finally { release(); await session.close(); }
});

it("discards a superseded reveal before dispatch and lets collapse proceed", async () => {
  const host = treeViewFixture();
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  host.update(async command => { if (command.kind === "filter") await held; });
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  try {
    const first = session.command({ kind: "filter", query: "src" });
    await expect.poll(() => host.commands.length).toBe(1);
    const abort = new AbortController();
    const reveal = session.command({ kind: "reveal", address: { root_id: "r1", path: "old.ts" } }, undefined, abort.signal);
    const cancelled = expect(reveal).rejects.toMatchObject({ name: "AbortError" });
    abort.abort(); await cancelled;
    const collapse = session.command({ kind: "disclose", disclosures: [{ address: { root_id: "r1", path: "." }, open: false, recursive: true }] });
    release(); await first; await collapse;
    expect(host.commands.map(command => command.kind)).toEqual(["filter", "disclose"]);
  } finally { release(); await session.close(); }
});

it("settles readers on a failed acceptance and explicitly retries its persistence", async () => {
  const host = treeViewFixture();
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.ready();
    vi.mocked(saveFilesTreeIntent).mockRejectedValueOnce(new Error("Disk unavailable"));
    await expect(session.expand([{ root_id: "r1", path: "." }])).rejects.toThrow("Disk unavailable");
    await expect(session.ready()).rejects.toThrow("Disk unavailable");
    expect(session.activity().pendingAcceptance).toBe(host.state().intent_revision);
    await session.retryPreparation();
    expect((await session.ready()).intent_revision).toBe(host.state().intent_revision);
    expect(session.activity().pendingAcceptance).toBeUndefined();
    expect(host.commands).toHaveLength(1);
  } finally { detach(); await session.close(); }
});

it("persists a dispatched reveal after its caller cancels before applying the next action", async () => {
  const host = treeViewFixture();
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  host.update(async command => { if (command.kind === "reveal") await held; });
  const session = new FilesTreeSession(host.client, "project", "ws1", undefined, {});
  const releasePresentation = vi.fn();
  vi.spyOn(session, "retainPresentation").mockReturnValue(releasePresentation);
  try {
    const abort = new AbortController();
    const reveal = session.command({ kind: "reveal", address: { root_id: "r1", path: "README.md" } }, "presented", abort.signal);
    const cancelled = expect(reveal).rejects.toMatchObject({ name: "AbortError" });
    await expect.poll(() => host.commands.length).toBe(1);
    abort.abort(); await cancelled;
    expect(releasePresentation).not.toHaveBeenCalled();
    const collapse = session.command({ kind: "disclose", disclosures: [{ address: { root_id: "r1", path: "." }, open: false, recursive: true }] });
    const saved = vi.mocked(saveFilesTreeIntent).mock.calls.length;
    release(); await collapse;
    expect(releasePresentation).toHaveBeenCalledOnce();
    expect(vi.mocked(saveFilesTreeIntent).mock.calls.slice(saved).map(call => call[1])).toEqual(["intent-2", "intent-3"]);
    expect(host.commands.map(command => command.kind)).toEqual(["reveal", "disclose"]);
  } finally { release(); await session.close(); }
});

it("refuses incomplete host frames without replacing its complete presentation", async () => {
  const host = treeViewFixture();
  let incomplete = false;
  const client = { ...host.client, getSourceViewRows: async (...args: Parameters<typeof host.client.getSourceViewRows>) => {
    const frame = await host.client.getSourceViewRows(...args) as SourceTreeFrame;
    return incomplete ? { ...frame, rows: frame.rows.slice(1) } : frame;
  } };
  const session = new FilesTreeSession(client, "project", "ws1", undefined, {});
  const detach = session.attach();
  try {
    await session.frame(0);
    const previous = session.presentation();
    incomplete = true;
    host.notify(); await session.refresh();
    await expect(session.frameAt({ root_id: "r1", path: "." })).rejects.toThrow("incomplete viewport");
    expect(session.presentation()).toEqual(previous);
  } finally { detach(); await session.close(); }
});
