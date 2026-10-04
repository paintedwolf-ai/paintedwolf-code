import { createSourceViewsClient } from "../api/source-views-client.ts";
import type { ProjectRoot, SourceTreeCommand, SourceTreeRow, SourceTreeView, SourceTreeViewUpdate } from "../api/types.ts";
import { receiveSourceViewEvent } from "../ui/paged-view/source-view-session.ts";

export const TREE_ROOTS: ProjectRoot[] = [{ id: "r1", path: "/repo", label: "repo", is_primary: true,
  kind: "attached", added_at: "2026-01-01T00:00:00Z" }];
export function treeRow(path: string, kind: "file" | "directory" = "file", expanded = false): SourceTreeRow {
  return { address: { root_id: "r1", path }, name: path === "." ? "repo" : path.split("/").at(-1) ?? path,
    depth: path === "." ? 0 : path.split("/").length, kind, expanded };
}

/** Scripted host frames keep DOM tests independent of filesystem traversal. */
export function treeViewFixture(initial = [treeRow(".", "directory", true), treeRow("README.md")]) {
  let id = crypto.randomUUID();
  // The host answers an expired handle as no longer retained.
  const expired = new Set<string>();
  let rows = initial;
  let total = initial.length;
  let rowAt = (index: number): SourceTreeRow => initial[index]!;
  const knownRows = new Map<string, number>();
  let rank: ((path: string) => number) | undefined;
  let revision = 1;
  let intentRevision = 1;
  let workspace = "ws1";
  let intent: SourceTreeView["intent"] = {};
  const requests: { method: string; path: string; body?: unknown; signal?: AbortSignal | null }[] = [];
  const commands: SourceTreeCommand[] = [];
  let update: ((command: SourceTreeCommand) => void | Promise<void>) | undefined;
  let read: ((signal?: AbortSignal | null) => Promise<void>) | undefined;
  let complete = true;
  let loadingDirectories = 0;
  let failure: string | undefined;
  type Generation = { state: SourceTreeView; revision: string; rows: SourceTreeRow[]; total: number; rowAt: (index: number) => SourceTreeRow; rank?: (path: string) => number };
  // Captured generations remain immutable.
  const retained = new Map<string, Generation>();
  const state = (): SourceTreeView => ({ kind: "tree", id, workspace_id: workspace, roots: TREE_ROOTS,
    intent_revision: `intent-${intentRevision}`, projection_revision: `projection-${revision}`,
    state: failure ? "failed" : complete ? "ready" : "preparing", extent: { rows: total, complete },
    ...(failure ? { failure: { code: "preparation_failed" as const, message: failure } } : {}),
    expires_at: "2026-09-15T23:00:00Z", intent, loading_directories: loadingDirectories });
  // Each projection revision identifies one row sequence.
  const keep = () => {
    const key = `projection-${revision}`;
    if (!retained.has(key)) retained.set(key, { state: structuredClone(state()), revision: key, rows, total, rowAt, rank });
  };
  const notify = () => {
    revision++;
    const current = state();
    receiveSourceViewEvent({ view_id: id, kind: "tree", intent_revision: current.intent_revision,
      projection_revision: current.projection_revision, invalidated: false, terminal: current.extent.complete });
  };
  const presentations = new Map<string, Generation>();
  const presentationFor = (url: URL) => {
    const key = url.pathname.split("/").at(-2) ?? "";
    const value = presentations.get(key);
    if (!value) throw new Error("Missing scripted presentation fixture");
    return value;
  };
  const client = createSourceViewsClient(async <T>(raw: string, init?: RequestInit): Promise<T> => {
    const url = new URL(raw, "http://fixture");
    const method = init?.method ?? "GET";
    const body = typeof init?.body === "string" ? JSON.parse(init.body) as Record<string, unknown> : undefined;
    requests.push({ method, path: raw, body, signal: init?.signal });
    if ([...expired].some(handle => url.pathname.includes(`/source/views/${handle}`))) {
      throw Object.assign(new Error("Source view not found"), { code: "source_view_not_found" });
    }
    if (method === "POST" && url.pathname.endsWith("/views")) {
      workspace = body?.workspace_id as string ?? "ws1";
      if (expired.has(id)) id = crypto.randomUUID();
    }
    if (url.pathname.endsWith("/presentations") && method === "POST") {
      if (!complete) throw Object.assign(new Error("Preparing"), { code: "source_view_preparing" });
      keep();
      const presentation = crypto.randomUUID(), saved = retained.get(state().projection_revision);
      if (!saved) throw new Error("Missing scripted generation fixture");
      presentations.set(presentation, saved);
      return { id: presentation, view: saved.state } as T;
    }
    if (method === "DELETE" && url.pathname.includes("/presentations/")) {
      presentations.delete(url.pathname.split("/").at(-1) ?? ""); return undefined as T;
    }
    if (url.pathname.endsWith("/rows")) {
      await read?.(init?.signal);
      init?.signal?.throwIfAborted();
      let start = Number(url.searchParams.get("offset") ?? 0);
      const anchor = url.searchParams.get("anchor");
      const answering = presentationFor(url);
      let target: number | undefined;
      if (anchor) {
        const address = JSON.parse(atob(anchor.replaceAll("-", "+").replaceAll("_", "/"))) as { path: string };
        let index = answering.rank?.(address.path) ?? answering.rows.findIndex(row => row.address.path === address.path);
        if (index < 0 || index >= answering.total || answering.rowAt(index)?.address.path !== address.path) {
          const known = knownRows.get(address.path);
          index = known !== undefined && known < answering.total && answering.rowAt(known)?.address.path === address.path ? known : 0;
        }
        target = Math.min(answering.total - 1, index + start);
        start = Math.max(0, target - Number(url.searchParams.get("context_before") ?? 0));
      }
      const end = Math.max(start, Math.min(answering.total, start + Number(url.searchParams.get("limit") ?? 200)));
      return { kind: "tree", view_id: id, intent_revision: answering.state.intent_revision, projection_revision: answering.revision,
        target, extent: { rows: answering.total, complete: true }, span: { start, end }, anchor: answering.rowAt(start)?.address ?? { root_id: "r1", path: "." },
        rows: Array.from({ length: end - start }, (_, i) => {
          const row = answering.rowAt(start + i); knownRows.set(row.address.path, start + i); return row;
        }), ancestors: start > 0 ? [{ index: 0, end: answering.total, row: answering.rowAt(0) }] : [] } as T;
    }
    if (method === "POST" && url.pathname.endsWith("/apply")) {
      const request = body as SourceTreeViewUpdate;
      commands.push(request.command);
      let command = request.command;
      if (command.kind === "toggle") {
        const address = command.address;
        const open = rows.find(row => row.address.root_id === address.root_id && row.address.path === address.path)?.expanded ?? false;
        command = { kind: "disclose", disclosures: open && command.collapse_descendants
          ? [{ address, open: false, recursive: true }, { address, open: true, recursive: false }]
          : [{ address, open: !open, recursive: false }] };
      }
      if (command.kind === "disclose") intent = { ...intent, disclosures: [...(intent.disclosures ?? []), ...command.disclosures] };
      if (command.kind === "filter") intent = { ...intent, filter: command.query };
      if (command.kind === "review") intent = { ...intent, review: command.scope };
      await update?.(command);

      intentRevision++; revision++;
    }
    if (url.pathname.endsWith("/locate")) {
      const anchor = url.searchParams.get("anchor");
      if (!anchor) throw new Error("Missing fixture locate anchor");
      const answering = presentationFor(url);
      const encoded = anchor.replaceAll("-", "+").replaceAll("_", "/");
      const address = JSON.parse(atob(encoded)) as { path: string };
      let index = answering.rank?.(address.path) ?? -1;
      for (let at = 0; index < 0 && at < Math.min(answering.total, 100_000); at++) {
        if (answering.rowAt(at)?.address.path === address.path) { index = at; break; }
      }
      return { kind: "tree", view_id: id, projection_revision: answering.revision, address,
        index: Math.max(0, index), visible: index >= 0, pending: false } as T;
    }
    keep();
    return state() as T;
  });
  return { client, get id() { return id; }, requests, commands, state, notify,
    /** Ends the current handle; the next create answers with a new view. */
    expire: () => { expired.add(id); revision++; },
    update: (handler: typeof update) => { update = handler; },
    read: (handler: typeof read) => { read = handler; },
    rows: (next: SourceTreeRow[]) => { knownRows.clear(); rows = next; rank = undefined; total = rows.length; rowAt = index => next[index]!; },
    large: (count: number, select: typeof rowAt, locate?: (path: string) => number) => { knownRows.clear(); total = count; rowAt = select; rank = locate; },
    failed: (message: string | undefined) => { failure = message; },
    preparing: (active: boolean, directories = active ? 1 : 0) => { complete = !active; loadingDirectories = directories; },
  };
}
