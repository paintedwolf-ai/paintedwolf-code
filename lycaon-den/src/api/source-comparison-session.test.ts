import { expect, it } from "vitest";
import { createSourceViewsClient } from "./source-views-client.ts";
import { SourceComparisonSession } from "./source-comparison-session.ts";
import type { SourceComparisonView, SourceComparisonViewCreate, SourceComparisonViewUpdate, SourceReaderRow } from "./types.ts";

function fixture(count = 501, rowText = (index: number) => `${index}\n`) {
  const views = new Map<string, SourceComparisonView>();
  const presentations = new Map<string, SourceComparisonView>();
  const created: SourceComparisonViewCreate[] = [];
  const updates: SourceComparisonViewUpdate[] = [];
  const frames: { id: string; offset: number; limit: number }[] = [];
  const released: string[] = [];
  const client = createSourceViewsClient(async <T>(raw: string, init?: RequestInit): Promise<T> => {
    const url = new URL(raw, "http://fixture");
    if (url.pathname.endsWith("/presentations") && init?.method === "POST") {
      const id = url.pathname.split("/views/")[1]!.split("/")[0]!;
      const state = views.get(id);
      expect(state).toBeDefined();
      const presentation = crypto.randomUUID();
      const saved = structuredClone(state!);
      presentations.set(presentation, saved);
      return { id: presentation, view: saved } as T;
    }
    if (url.pathname.includes("/interests/")) return undefined as T;
    if (url.pathname.includes("/presentations/") && init?.method === "DELETE") {
      presentations.delete(url.pathname.split("/").at(-1) ?? ""); return undefined as T;
    }
    if (init?.method === "POST" && url.pathname.endsWith("/views")) {
      const request = JSON.parse(String(init.body)) as SourceComparisonViewCreate;
      created.push(request);
      const id = `view-${created.length}`;
      const state: SourceComparisonView = { id, kind: "comparison", state: "ready", intent: request.intent,
        intent_revision: "one", projection_revision: "one", expires_at: "2026-09-16T23:00:00Z",
        extent: { rows: count, complete: true }, comparison: { in_range: true, location_changed: false,
          summary: { before: { path: "a.ts", lines: count, sha256: "a", availability: "available" },
            after: { path: "a.ts", lines: count, sha256: "b", availability: "available" }, rows: count,
            added: 1, removed: 1, change_areas: [], change_area_count: 1 } } };
      views.set(id, state); return state as T;
    }
    const id = url.pathname.split("/views/")[1]!.split("/")[0]!;
    const state = (url.pathname.includes("/presentations/") ? presentations.get(url.pathname.split("/").at(-2) ?? "") : views.get(id))!;
    if (init?.method === "DELETE") { released.push(id); return undefined as T; }
    if (init?.method === "POST" && url.pathname.endsWith("/apply")) {
      const request = JSON.parse(String(init.body)) as SourceComparisonViewUpdate;
      updates.push(request);
      const next = { ...state, intent: request.intent, intent_revision: String(updates.length), projection_revision: String(updates.length) };
      views.set(id, next); return next as T;
    }
    if (url.pathname.endsWith("/rows")) {
      const encoded = url.searchParams.get("anchor");
      const offset = encoded ? (JSON.parse(atob(encoded.replaceAll("-", "+").replaceAll("_", "/"))) as { row: number }).row : Number(url.searchParams.get("offset") ?? 0);
      const limit = Number(url.searchParams.get("limit") ?? 200);
      frames.push({ id, offset, limit });
      const end = Math.min(count, offset + limit);
      const rows = Array.from({ length: end - offset }, (_, i): SourceReaderRow => ({ index: offset + i,
        end: offset + i + 1, kind: "equal", text: rowText(offset + i), before_line: offset + i + 1,
        after_line: offset + i + 1, changed: [] }));
      return { kind: "comparison", view_id: id, intent_revision: state.intent_revision,
        projection_revision: state.projection_revision, extent: state.extent, span: { start: offset, end }, anchor: { row: offset }, rows } as T;
    }
    return state as T;
  });
  return { client, created, updates, frames, released };
}

it("copies through a bounded independent side view and releases it", async () => {
  const host = fixture();
  const visible = new SourceComparisonSession(host.client, "project", { kind: "effect", effect_id: "effect" });
  const detach = visible.attach();
  await visible.ready();
  expect(await visible.text("before")).toBe(Array.from({ length: 501 }, (_, i) => `${i}\n`).join(""));
  expect(visible.state()?.intent.mode).toBe("changes");
  expect(host.created[1]?.source).toEqual({ kind: "retained", view_id: "view-1", comparison: "before" });
  expect(host.created[1]?.intent).toEqual({ mode: "before" });
  expect(host.frames).toEqual([0, 200, 400].map(offset => ({ id: "view-2", offset, limit: 200 })));
  expect(host.released).toEqual(["view-2"]);
  detach(); await visible.close();
});

it("copies source selection boundaries without loading preceding pages", async () => {
  const host = fixture(10_000_000);
  const view = new SourceComparisonSession(host.client, "project", { kind: "version", version_id: "version" });
  expect(await view.text("after", { side: "after", row: 8_000_000, offset: 2 },
    { side: "after", row: 8_000_001, offset: 3 })).toBe("00000\n800");
  expect(host.frames).toEqual([{ id: "view-2", offset: 8_000_000, limit: 200 }]);
  expect(host.released).toEqual(["view-2"]);
  await view.close();
});

it("coalesces overlapping fold intent while preserving independent modes", async () => {
  const host = fixture();
  const view = new SourceComparisonSession(host.client, "project", { kind: "effect", effect_id: "effect" });
  await view.unfold(20, 30);
  await view.unfold(10, 25);
  await view.mode("split");
  expect(view.state()?.intent).toEqual({ mode: "split", expanded: [{ start: 10, end: 30 }] });
  expect(host.updates.map(update => update.expected_intent_revision)).toEqual(["one", "1", "2"]);
  await view.close();
});


it("bounds huge-file copy accumulation and releases its independent view on refusal", async () => {
  const host = fixture(3000, () => "x".repeat(4096));
  const view = new SourceComparisonSession(host.client, "project", { kind: "current", root_id: "root", path: "huge.txt" });
  await expect(view.text("after")).rejects.toThrow("too large");
  expect(host.frames.length).toBeLessThanOrEqual(11);
  expect(host.released).toEqual(["view-2"]);
  await view.close();
});
