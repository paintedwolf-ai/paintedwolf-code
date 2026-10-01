import type { Page } from "@playwright/test";
import type { Error as WireError, SourceTreeAddress, SourceTreeFrame, SourceTreeRow, SourceTreeView, SourceTreeViewUpdate } from "../src/api/types.ts";

/** A complete million-row tree exercises browser geometry without a catalog scan. */
export async function installLargeTreeFrames(page: Page, beforeFrame?: () => Promise<void>): Promise<void> {
  let state: SourceTreeView | undefined;
  let expanded = false;
  let revision = 1;
  const presentations = new Map<string, { view: SourceTreeView; expanded: boolean }>();
  const count = (open = expanded) => open ? 1_000_001 : 101;
  const rowAt = (index: number, view = state!, open = expanded): SourceTreeRow => {
    const group = open ? Math.floor((index - 1) / 10_000) : index - 1;
    const child = open ? (index - 1) % 10_000 : 0;
    const folder = `folder-${String(group).padStart(3, "0")}`;
    const path = index === 0 ? "." : child === 0 ? folder : `${folder}/file-${child - 1}.ts`;
    return { address: { root_id: view.roots[0]!.id, path }, name: index === 0 ? view.roots[0]!.label : path.split("/").at(-1)!,
      kind: child === 0 || index === 0 ? "directory" : "file", depth: index === 0 ? 0 : child === 0 ? 1 : 2,
      expanded: index === 0 || (child === 0 && open) };
  };
  const indexOf = (address: SourceTreeAddress, open = expanded) => {
    if (address.path === ".") return 0;
    const match = /^folder-(\d+)(?:\/file-(\d+)\.ts)?$/.exec(address.path);
    if (!match) throw new Error(`Unexpected fixture address: ${address.path}`);
    return open ? 1 + Number(match[1]) * 10_000 + (match[2] === undefined ? 0 : Number(match[2]) + 1) : 1 + Number(match[1]);
  };
  await page.route("**/source/views**", async route => {
    const request = route.request(), url = new URL(request.url());
    if (url.pathname.includes("/interests/")) {
      await route.fulfill({ status: 204 });
      return;
    }
    if (url.pathname.endsWith("/presentations") && request.method() === "POST") {
      const id = crypto.randomUUID();
      const view = structuredClone(state!);
      presentations.set(id, { view, expanded });
      await route.fulfill({ status: 201, json: { id, view } });
      return;
    }
    const presentationID = url.pathname.split("/presentations/")[1]?.split("/")[0];
    if (request.method() === "DELETE" && presentationID) {
      presentations.delete(presentationID);
      await route.fulfill({ status: 204 });
      return;
    }
    if (request.method() === "POST" && url.pathname.endsWith("/views")) {
      const response = await route.fetch();
      state = { ...await response.json() as SourceTreeView, state: "ready", loading_directories: 0,
        extent: { rows: count(), complete: true }, projection_revision: `fixture-${revision}` };
      await route.fulfill({ json: state });
      return;
    }
    if (!state || request.method() === "DELETE") { await route.continue(); return; }
    if (request.method() === "POST" && url.pathname.endsWith("/apply")) {
      const { command } = request.postDataJSON() as SourceTreeViewUpdate;
      if (command.kind === "disclose") {
        for (const rule of command.disclosures) if (rule.recursive) expanded = rule.open;
        state.intent = { ...state.intent, disclosures: [...(state.intent.disclosures ?? []), ...command.disclosures] };
      }
      revision++;
      state = { ...state, intent_revision: `intent-${revision}`, projection_revision: `fixture-${revision}`,
        extent: { rows: count(), complete: true } };
    }
    if (!url.pathname.endsWith("/rows")) { await route.fulfill({ json: state }); return; }
    const held = presentations.get(presentationID!);
    if (!held) { await route.fulfill({ status: 404, json: { code: "source_view_not_found", message: "Source view not found." } satisfies WireError }); return; }
    const view = held.view, open = held.expanded;
    const total = count(open);
    const row = (index: number) => rowAt(index, view, open);
    const anchor = url.searchParams.get("anchor");
    let start = Number(url.searchParams.get("offset") ?? 0);
    if (anchor) start += indexOf(JSON.parse(Buffer.from(anchor, "base64url").toString()) as SourceTreeAddress, open);
    const target = anchor ? Math.min(total - 1, start) : undefined;
    start = Math.max(0, (target ?? start) - Number(url.searchParams.get("context_before") ?? 0));
    const end = Math.min(total, start + Number(url.searchParams.get("limit") ?? 200));
    const ancestors: SourceTreeFrame["ancestors"] = start > 0 ? [{ index: 0, end: total, row: row(0) }] : [];
    if (open && start > 0) {
      const parent = 1 + Math.floor((start - 1) / 10_000) * 10_000;
      if (start > parent) ancestors.push({ index: parent, end: parent + 10_000, row: row(parent) });
    }
    const frame: SourceTreeFrame = { kind: "tree", view_id: view.id, intent_revision: view.intent_revision,
      projection_revision: view.projection_revision, extent: view.extent, anchor: row(start).address,
      target, span: { start, end }, ancestors, rows: Array.from({ length: end - start }, (_, index) => row(start + index)) };
    await beforeFrame?.();
    await route.fulfill({ json: frame });
  });
}
