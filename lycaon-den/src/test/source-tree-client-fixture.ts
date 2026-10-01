import { vi } from "vitest";
import type { LycaonClient } from "../api/client.ts";
import type { SourceViewsClient } from "../api/source-views-client.ts";
import type { ProjectRoot, SourceTreeFrame, SourceTreeRow, SourceTreeView, SourceWorkspace } from "../api/types.ts";
import { SourceWorkspaceMismatchError } from "../files/source/source-workspace-identity.ts";

/** Stage fixtures share directory data between browsing controls and host tree frames. */
export function sourceTreeClientFixture(client: () => LycaonClient, workspaceReader?: LycaonClient["getSourceWorkspace"]) {
  const workspaces = new Map<string, SourceWorkspace>();
  const views = new Map<string, { view: SourceTreeView; rows: SourceTreeRow[]; project: string; session?: string }>();
  const getSourceWorkspace = workspaceReader && vi.fn(async (...args: Parameters<LycaonClient["getSourceWorkspace"]>) => {
    const result = await workspaceReader(...args);
    workspaces.set(result.workspace_id, result);
    return result;
  });
  function retained(id: string) {
    const entry = views.get(id);
    if (!entry) throw new Error("Missing tree view fixture");
    return entry;
  }
  async function populate(held: NonNullable<ReturnType<typeof views.get>>) {
    const rows: SourceTreeRow[] = [];
    for (const root of held.view.roots) {
      async function directory(path: string, depth: number, name: string) {
        let expanded = path === ".";
        for (const disclosure of held.view.intent.disclosures ?? []) {
          if (disclosure.address.root_id !== root.id) continue;
          const parent = disclosure.address.path;
          if (parent === path || disclosure.recursive && (parent === "." || path.startsWith(`${parent}/`))) expanded = disclosure.open;
        }
        rows.push({ address: { root_id: root.id, path }, name, depth, kind: "directory", expanded });
        if (!expanded) return;
        const listing = await client().browseProjectSource(held.project, { rootId: root.id, dir: path }, held.session);
        if (listing.workspace_id !== held.view.workspace_id) throw new SourceWorkspaceMismatchError(held.view.workspace_id, listing.workspace_id);
        for (const entry of listing.entries) {
          const child = path === "." ? entry.name : `${path}/${entry.name}`;
          if (entry.is_dir) await directory(child, depth + 1, entry.name);
          else rows.push({ address: { root_id: root.id, path: child }, name: entry.name, depth: depth + 1, kind: "file", expanded: false });
        }
      }
      await directory(".", 0, root.label);
    }
    held.rows = rows;
    held.view.extent = { rows: rows.length, complete: true };
  }
  const presentations = new Map<string, ReturnType<typeof retained>>();
  const presentationFor = (id: string) => {
    const value = presentations.get(id);
    if (!value) throw new Error("Missing source presentation fixture");
    return value;
  };
  const methods: Pick<SourceViewsClient, "createSourcePresentation" | "releaseSourcePresentation" | "createSourceView" | "getSourceView" | "getSourceViewRows" | "applySourceViewIntent" | "releaseSourceView" | "locateSourceView"> = {
    async createSourceView(project, request) {
      if (request.kind !== "tree") throw new Error("Expected tree fixture");
      const workspace = workspaces.get(request.workspace_id);
      const roots: ProjectRoot[] = (workspace?.roots ?? []).map((root, index) => ({ ...root, label: root.path.split("/").at(-1) ?? "", is_primary: index === 0, kind: "attached", added_at: "2026-01-01T00:00:00Z" }));
      const view: SourceTreeView = { kind: "tree", id: crypto.randomUUID(), workspace_id: request.workspace_id, roots,
        intent: request.intent, state: "ready", intent_revision: "1", projection_revision: "1", expires_at: "2027-01-01T00:00:00Z",
        extent: { rows: 0, complete: true }, loading_directories: 0 };
      const held = { view, rows: [], project, session: request.session_id };
      await populate(held);
      views.set(view.id, held);
      return view;
    },
    async createSourcePresentation(_project, id, request) {
      const value = retained(id);
      if (value.view.intent_revision !== request.intent_revision) throw Object.assign(new Error("Changed intent"), { code: "source_view_revision_changed" });
      const presentation = crypto.randomUUID(), snapshot = structuredClone(value);
      presentations.set(presentation, snapshot);
      return { id: presentation, view: snapshot.view };
    },
    async releaseSourcePresentation(_project, _id, presentation) { presentations.delete(presentation); },
    async getSourceView(_project, id) { return retained(id).view; },
    async releaseSourceView(_project, id) { views.delete(id); },
    async getSourceViewRows(_project, id, query) {
      const { view, rows } = presentationFor(query.presentation_id);
      const anchor = query.anchor && "path" in query.anchor ? query.anchor : undefined;
      const target = anchor ? Math.min(rows.length - 1, Math.max(0, rows.findIndex(row => row.address.root_id === anchor.root_id && row.address.path === anchor.path)) + (query.offset ?? 0)) : undefined;
      const start = target === undefined ? query.offset ?? 0 : Math.max(0, target - (query.context_before ?? 0));
      const end = Math.min(rows.length, start + (query.limit ?? 200));
      const frame: SourceTreeFrame = { kind: "tree", view_id: id, intent_revision: view.intent_revision, projection_revision: view.projection_revision,
        target, extent: view.extent, span: { start, end }, anchor: rows[start]?.address ?? { root_id: view.roots[0]?.id ?? "", path: "." }, rows: rows.slice(start, end), ancestors: [] };
      return frame;
    },
    async applySourceViewIntent(_project, id, request) {
      if (request.kind !== "tree") throw new Error("Expected tree command");
      const held = retained(id);
      let command = request.command;
      if (command.kind === "toggle") {
        const address = command.address;
        const open = held.rows.find(row => row.address.root_id === address.root_id && row.address.path === address.path)?.expanded ?? false;
        command = { kind: "disclose", disclosures: open && command.collapse_descendants
          ? [{ address, open: false, recursive: true }, { address, open: true, recursive: false }]
          : [{ address, open: !open, recursive: false }] };
      }
      if (command.kind === "disclose") held.view.intent = { ...held.view.intent, disclosures: [...(held.view.intent.disclosures ?? []), ...command.disclosures] };
      if (command.kind === "reveal") {
        const parts = command.address.path.split("/");
        const disclosures = [".", ...parts.slice(0, -1).map((_part, index) => parts.slice(0, index + 1).join("/"))]
          .map(path => ({ address: { root_id: command.address.root_id, path }, open: true, recursive: false }));
        held.view.intent = { ...held.view.intent, disclosures: [...(held.view.intent.disclosures ?? []), ...disclosures] };
      }
      if (command.kind === "filter") held.view.intent = { ...held.view.intent, filter: command.query };
      if (command.kind === "review") held.view.intent = { ...held.view.intent, review: command.scope };
      held.view = { ...held.view, intent_revision: String(Number(held.view.intent_revision) + 1), projection_revision: String(Number(held.view.projection_revision) + 1) };
      await populate(held);
      return held.view;
    },
    async locateSourceView(_project, id, query) {
      const { view, rows } = presentationFor(query.presentation_id);
      if (!query.anchor || !("path" in query.anchor)) throw new Error("Expected tree address");
      const address = query.anchor;
      const index = rows.findIndex(row => row.address.root_id === address.root_id && row.address.path === address.path);
      return { kind: "tree", view_id: id, projection_revision: view.projection_revision, address, index: Math.max(0, index), visible: index >= 0, pending: false };
    },
  };
  return { methods, getSourceWorkspace, has: (id: string) => views.has(id) };
}
