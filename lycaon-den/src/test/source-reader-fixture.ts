import { createHash } from "node:crypto";
import { diffLines } from "diff";
import type { SourceComparison, SourceComparisonDetails, SourceComparisonFrame, SourceComparisonIntent, SourceComparisonSelector, SourceComparisonSummary, SourceComparisonView, SourceReaderRow } from "../api/types.ts";
import type { LycaonClient } from "../api/client.ts";
import type { ComparisonReference, ComparisonSnapshot } from "../api/source-reader.ts";
import { sourceReaderAccess } from "../api/source-reader.ts";
import { stubClient } from "./client-fixture.ts";
import { sourceChangeFromPresence } from "../components/source/reader/source-reader-change.ts";
import { retainedFileEditFixtures } from "./file-edit-fixture.ts";

type Document = { summary: SourceComparisonSummary; rows: SourceReaderRow[]; before: string | null; after: string | null; details: SourceComparisonDetails };
const views = new Map<string, { view: SourceComparisonView; document: Document }>();
const versions = new Map<string, string>();
const hash = (text: string) => createHash("sha256").update(text).digest("hex");

export function sourceReaderFixture(resolve?: (project: string, source: SourceComparisonSelector, sessionId?: string) => Promise<SourceComparison | ComparisonSnapshot>) {
  const prepare = (before: string | null, after: string | null, path = "file.txt"): ComparisonReference => {
    let beforeLine = 1, afterLine = 1;
    const rows: SourceReaderRow[] = [];
    const areas: SourceComparisonSummary["change_areas"] = [];
    let area: SourceComparisonSummary["change_areas"][number] | undefined;
    for (const change of diffLines(before ?? "", after ?? "")) {
      const lines = change.value.match(/[^\n]*\n|[^\n]+$/g) ?? [];
      if (change.added || change.removed) {
        if (!area) { area = { after_line: afterLine, added: 0, removed: 0 }; areas.push(area); }
        if (change.added) area.added += lines.length; else area.removed += lines.length;
      } else area = undefined;
      for (const text of lines) rows.push({ index: rows.length, end: rows.length + 1,
        kind: change.added ? "insert" : change.removed ? "delete" : "equal", text,
        before_line: change.added ? 0 : beforeLine++, after_line: change.removed ? 0 : afterLine++, changed: [] });
    }
    const side = (text: string | null) => ({ path, sha256: hash(text ?? ""), lines: (text?.match(/[^\n]*\n|[^\n]+$/g) ?? []).length, availability: text === null ? "absent" : "available" });
    const summary: SourceComparisonSummary = { change_areas: areas.slice(0, 6), change_area_count: areas.length,
      before: side(before), after: side(after), rows: rows.length,
      added: rows.filter(row => row.kind === "insert").length, removed: rows.filter(row => row.kind === "delete").length };
    const document: Document = { summary, rows, before, after, details: { summary, in_range: true, location_changed: false } };
    const view = create(document, { mode: "changes" });
    return { view, source: { kind: "text", path, before, after } };
  };
  function projected(document: Document, intent: SourceComparisonIntent): SourceReaderRow[] {
    const visible = new Set<number>();
    for (const row of document.rows) if (row.kind !== "equal") {
      for (let index = Math.max(0, row.index - 3); index <= row.index + 3; index++) visible.add(index);
    }
    const result: SourceReaderRow[] = [];
    const condensed = intent.mode === "changes" || intent.mode === "split";
    const shown = (index: number) => !condensed || !visible.size || visible.has(index) || intent.expanded?.some(range => index >= range.start && index < range.end);
    for (let at = 0; at < document.rows.length;) {
      const row = document.rows[at++]!;
      if (intent.mode === "before" && row.kind === "insert" || intent.mode === "after" && row.kind === "delete") continue;
      if (!shown(row.index)) {
        while (at < document.rows.length && !shown(at)) at++;
        result.push({ ...row, kind: "gap", text: "", end: at });
      } else result.push(row);
    }
    return result;
  }
  function create(document: Document, intent: SourceComparisonIntent): SourceComparisonView {
    const id = crypto.randomUUID();
    const view: SourceComparisonView = { kind: "comparison", id, state: "ready", intent, comparison: document.details,
      intent_revision: "1", projection_revision: "1", expires_at: "2027-01-01T00:00:00Z", extent: { rows: projected(document, intent).length, complete: true } };
    views.set(id, { view, document }); return view;
  }
  function held(id: string) {
    const value = views.get(id);
    if (!value) throw Object.assign(new Error("Missing source view fixture"), { code: "source_view_not_found" });
    return value;
  }
  const comparison = (value: SourceComparison): ComparisonSnapshot => {
    const endpoint = (side: NonNullable<SourceComparison["before"]>) => { const { content: _content, ...rest } = side; return rest; };
    if (!value.in_range || !value.before || !value.after) return { in_range: false, location_changed: false };
    const reference = prepare(value.before.availability === "absent" ? null : value.before.content, value.after.availability === "absent" ? null : value.after.content, value.after.path);
    const details = { ...value, before: endpoint(value.before), after: endpoint(value.after), summary: reference.view.comparison!.summary };
    held(reference.view.id).document.details = details;
    reference.view.comparison = details;
    return { ...details, reference };
  };
  const createSourceView: LycaonClient["createSourceView"] = async (project, request) => {
    if (request.kind !== "comparison") throw new Error("Expected comparison fixture");
    const source = request.source;
    const version = source.kind === "version" ? versions.get(source.version_id) : undefined;
    let reference: ComparisonReference | undefined;
    if (source.kind === "retained") {
      const original = held(source.view_id).document;
      return create(original, request.intent);
    }
    if (source.kind === "text") reference = prepare(source.before, source.after, source.path);
    else if (source.kind === "chat") {
      const first = retainedFileEditFixtures.get(source.first.message_id), last = retainedFileEditFixtures.get(source.last.message_id);
      if (!first || !last) throw new Error("Missing retained file edit fixture");
      reference = prepare(first.before ?? null, last.deleted ? null : last.after, last.path);
    } else if (version !== undefined) {
      return create(held(version).document, request.intent);
    } else if (resolve) {
      const value = await resolve(project, source, request.session_id);
      const snapshot = value.before && "content" in value.before ? comparison(value as SourceComparison) : value as ComparisonSnapshot;
      reference = snapshot.reference;
      if (!reference) {
        const empty = prepare(null, null);
        const value = held(empty.view.id); value.document.details = snapshot;
        return create(value.document, request.intent);
      }
    }
    if (!reference) throw new Error(`Unconfigured comparison selector: ${source.kind}`);
    return create(held(reference.view.id).document, request.intent);
  };
  const presentations = new Map<string, ReturnType<typeof held>>();
  const presentationFor = (id: string) => {
    const value = presentations.get(id);
    if (!value) throw new Error("Missing source presentation fixture");
    return value;
  };
  const createSourcePresentation: LycaonClient["createSourcePresentation"] = async (_project, id, request) => {
    const value = held(id);
    if (value.view.intent_revision !== request.intent_revision) throw Object.assign(new Error("Changed intent"), { code: "source_view_revision_changed" });
    const presentation = crypto.randomUUID();
    const snapshot = structuredClone(value);
    presentations.set(presentation, snapshot);
    return { id: presentation, view: snapshot.view };
  };
  const releaseSourcePresentation: LycaonClient["releaseSourcePresentation"] = async (_project, _id, presentation) => { presentations.delete(presentation); };
  const getSourceView: LycaonClient["getSourceView"] = async (_project, id) => held(id).view;
  const releaseSourceView: LycaonClient["releaseSourceView"] = async (_project, id) => { views.delete(id); };
  const applySourceViewIntent: LycaonClient["applySourceViewIntent"] = async (_project, id, request) => {
    if (request.kind !== "comparison") throw new Error("Expected comparison intent");
    const value = held(id);
    if (request.expected_intent_revision !== value.view.intent_revision) throw Object.assign(new Error("Stale comparison intent"), { code: "source_view_revision_changed" });
    const revision = String(Number(value.view.intent_revision) + 1);
    value.view = { ...value.view, intent: request.intent, intent_revision: revision, projection_revision: revision,
      extent: { rows: projected(value.document, request.intent).length, complete: true } };
    return value.view;
  };
  const getSourceViewRows: LycaonClient["getSourceViewRows"] = async (_project, id, query) => {
    const { view, document } = presentationFor(query.presentation_id), rows = projected(document, view.intent);
    const anchor = query.anchor && "row" in query.anchor ? query.anchor.row : undefined;
    const start = anchor === undefined ? query.offset ?? 0 : Math.max(0, rows.findIndex(row => row.index <= anchor && row.end > anchor));
    const end = Math.min(rows.length, start + (query.limit ?? 200));
    const result: SourceComparisonFrame = { kind: "comparison", view_id: id, intent_revision: view.intent_revision,
      projection_revision: view.projection_revision, extent: view.extent, span: { start, end }, rows: rows.slice(start, end), anchor: { row: rows[start]?.index ?? 0 } };
    return result;
  };
  const locateSourceView: LycaonClient["locateSourceView"] = async (_project, id, query) => {
    const { view, document } = presentationFor(query.presentation_id), rows = projected(document, view.intent);
    const sourceRow = query.line !== undefined ? document.rows.find(row => (query.side === "before" ? row.before_line : row.after_line) === query.line)?.index ?? 0 : query.anchor && "row" in query.anchor ? query.anchor.row : 0;
    const index = Math.max(0, rows.findIndex(row => row.index <= sourceRow && row.end > sourceRow));
    return { kind: "comparison", view_id: id, projection_revision: view.projection_revision, index, visible: rows[index]?.kind !== "gap", anchor: { row: sourceRow } };
  };
  const searchSourceView: LycaonClient["searchSourceView"] = async (_project, id, query) => {
    const { view, document } = presentationFor(query.presentation_id);
    const term = query.q;
    const matches = document.rows.flatMap(row => { const at = row.text.indexOf(term); return at < 0 || term === "" ? [] : [{ row: row.index, from: at, to: at + term.length }]; });
    return { kind: "comparison", view_id: id, projection_revision: view.projection_revision, matches, next_cursor: "", complete: true };
  };
  const setRows = (reference: ComparisonReference, rows: SourceReaderRow[]) => {
    const value = held(reference.view.id); value.document.rows = rows; value.document.summary.rows = rows.length;
    value.view.extent.rows = projected(value.document, value.view.intent).length;
  };
  const publishRows = (reference: ComparisonReference, rows: SourceReaderRow[]) => {
    setRows(reference, rows);
    const document = held(reference.view.id).document;
    return [...views.values()].filter(value => value.document === document).map(value => {
      value.view = { ...value.view, projection_revision: String(Number(value.view.projection_revision) + 1) };
      return value.view;
    });
  };
  // Digests and views share comparison preparation.
  const digestSourceComparisons: LycaonClient["digestSourceComparisons"] = async (project, request) => ({
    digests: await Promise.all(request.sources.map(async source => {
      try {
        const view = await createSourceView(project, { kind: "comparison", operation_id: crypto.randomUUID(), client_id: "fixture",
          session_id: request.session_id, source, intent: { mode: "changes" } });
        if (view.kind !== "comparison") throw new Error("Expected a comparison view");
        const value = held(view.id);
        views.delete(view.id);
        if (view.comparison?.in_range === false || !view.comparison?.summary) return { in_range: false };
        const rows = projected(value.document, view.intent);
        return { in_range: true, summary: view.comparison.summary, changes_rows: rows.length, changes_folds: rows.filter(row => row.kind === "gap").length };
      } catch (cause) {
        return { in_range: false, failure: { code: "unavailable" as const, message: cause instanceof Error ? cause.message : String(cause) } };
      }
    })),
  });
  const replaceSourceViewportInterest: LycaonClient["replaceSourceViewportInterest"] = async () => {};
  const releaseSourceViewportInterest: LycaonClient["releaseSourceViewportInterest"] = async () => {};
  const methods = { replaceSourceViewportInterest, releaseSourceViewportInterest, createSourcePresentation, releaseSourcePresentation, createSourceView, getSourceView, releaseSourceView, applySourceViewIntent, getSourceViewRows, locateSourceView, searchSourceView, digestSourceComparisons };
  return { prepare, comparison, setRows, publishRows, methods, ...methods };
}

export function diffViewerFixture(request: Omit<import("../platform/navigation/in-app-diff.ts").DiffViewerRequest, "reader" | "change"> & { before: string | null; after: string | null }): import("../platform/navigation/in-app-diff.ts").DiffViewerRequest {
  const fixture = sourceReaderFixture();
  const { before, after, ...identity } = request;
  const reference = fixture.prepare(before, after, request.path);
  const methods = fixture.methods;
  return { ...identity, change: sourceChangeFromPresence(before !== null, after !== null), reader: sourceReaderAccess(stubClient(methods), "project", reference) };
}

export function deletedSourceFixture(value: { deleted_at: string; previous: NonNullable<SourceComparison["before"]> }): import("../api/types.ts").SourceDeletedFile {
  const { content, ...previous } = value.previous;
  const reference = sourceReaderFixture().prepare(content, null, previous.path);
  const version_id = crypto.randomUUID(); versions.set(version_id, reference.view.id);
  return { deleted_at: value.deleted_at, previous, source: { kind: "version", version_id } };
}
