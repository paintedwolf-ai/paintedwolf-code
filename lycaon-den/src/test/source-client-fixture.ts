import { sourceTreeClientFixture } from "./source-tree-client-fixture.ts";
import { sourceReaderFixture } from "./source-reader-fixture.ts";
import type { SourceComparison, SourceComparisonSelector } from "../api/types.ts";
import type { ProjectSourceReadResponse } from "../api/types.ts";
import type { LycaonClient } from "../api/client.ts";
import type { SourceComparisonTarget } from "../api/http-capabilities/source-history.ts";
import { stubClient, type ClientStubs } from "./client-fixture.ts";
import { LycaonApiError } from "../api/http.ts";
import { vi } from "vitest";
import type { ComparisonSnapshot } from "../api/source-reader.ts";
export type ComparisonLoader = (project: string, target: SourceComparisonTarget, options?: { sessionId?: string }) => Promise<SourceComparison | ComparisonSnapshot>;
export type FileClientStubs = ClientStubs & { readComparison?: unknown };

const fixtureMethods = new WeakSet<object>();
/** Open requests a fixture already classified, so a nested fixture opener does not read again. */
const classifiedOpens = new WeakSet<object>();
const servedSources = new Map<string, ProjectSourceReadResponse>();
const servedKey = (projectId: string, rootId: string, path: string) => `${projectId}\0${rootId}\0${path}`;

/** Document fixtures use the latest served source text. */
export function servedSource(projectId: string, rootId: string, path: string): ProjectSourceReadResponse | undefined {
  return servedSources.get(servedKey(projectId, rootId, path));
}

export function resetServedSourcesForTests(): void {
  servedSources.clear();
}

type PrepareDocument = (projectId: string, source: ProjectSourceReadResponse) => void;

/** Models the combined source endpoint using the same source and document fixtures. */
export function stubFilesClient<T extends FileClientStubs>(overrides: T & Record<Exclude<keyof T, keyof FileClientStubs>, never>, prepareDocument?: PrepareDocument): LycaonClient & T;
export function stubFilesClient(): LycaonClient;
export function stubFilesClient(overrides: FileClientStubs = {}, prepareDocument?: PrepareDocument): LycaonClient {
  const hostDocument = overrides.openEditorDocument as LycaonClient["openEditorDocument"] | undefined;
  // The host opens only editable text as a document and refuses any other file. A
  // client spread from this one reads the source through its own overrides.
  const openEditorDocument = vi.fn<LycaonClient["openEditorDocument"]>(async function (this: unknown, projectId, request, sessionId) {
    if (!classifiedOpens.has(request)) {
      const sourceClient = (this as LycaonClient | undefined) ?? client;
      const observed = await sourceClient.getProjectSource(projectId, request.path, {
        rootId: request.root_id, sessionId, decodeAs: request.decode_as, includeDeleted: true,
      });
      const source = { ...observed, file_id: observed.file_id ?? "", version_id: observed.version_id ?? "", root_id: observed.root_id ?? request.root_id };
      servedSources.set(servedKey(projectId, source.root_id, source.path), source);
      prepareDocument?.(projectId, source);
      if (!source.writable || source.binary || source.over_limit || !source.encoding || source.deleted) {
        throw new LycaonApiError("only editable text files can be opened as documents", 422, "source_binary");
      }
      classifiedOpens.add(request);
    }
    if (!hostDocument) throw new Error("Test client did not stub openEditorDocument");
    return hostDocument(projectId, request, sessionId);
  });
  const reader = sourceReaderFixture(async (project, source, sessionId) => {
    const read = "readComparison" in overrides ? (client as LycaonClient & { readComparison: ComparisonLoader }).readComparison : undefined;
    if (!read) throw new Error("Missing comparison fixture loader");
    return sessionId === undefined ? read(project, comparisonTarget(source)) : read(project, comparisonTarget(source), { sessionId });
  });
  const tree = sourceTreeClientFixture(() => client, overrides.getSourceWorkspace as LycaonClient["getSourceWorkspace"] | undefined);
  const methods = {
    ...reader.methods,
    listSourceOperations: async () => ({ operations: [] }),
    createSourcePresentation: ((project, id, request, signal) => tree.has(id) ? tree.methods.createSourcePresentation(project, id, request, signal) : reader.createSourcePresentation(project, id, request, signal)) as LycaonClient["createSourcePresentation"],
    releaseSourcePresentation: ((project, id, presentation) => tree.has(id) ? tree.methods.releaseSourcePresentation(project, id, presentation) : reader.releaseSourcePresentation(project, id, presentation)) as LycaonClient["releaseSourcePresentation"],
    createSourceView: ((project, request, signal) => request.kind === "tree"
      ? tree.methods.createSourceView(project, request, signal) : reader.createSourceView(project, request, signal)) as LycaonClient["createSourceView"],
    getSourceView: ((project, id, signal) => tree.has(id) ? tree.methods.getSourceView(project, id, signal) : reader.getSourceView(project, id, signal)) as LycaonClient["getSourceView"],
    getSourceViewRows: ((project, id, query, signal) => tree.has(id) ? tree.methods.getSourceViewRows(project, id, query, signal) : reader.getSourceViewRows(project, id, query, signal)) as LycaonClient["getSourceViewRows"],
    applySourceViewIntent: ((project, id, request) => tree.has(id) ? tree.methods.applySourceViewIntent(project, id, request) : reader.applySourceViewIntent(project, id, request)) as LycaonClient["applySourceViewIntent"],
    releaseSourceView: ((project, id) => tree.has(id) ? tree.methods.releaseSourceView(project, id) : reader.releaseSourceView(project, id)) as LycaonClient["releaseSourceView"],
    locateSourceView: ((project, id, query, signal) => tree.has(id) ? tree.methods.locateSourceView(project, id, query, signal) : reader.locateSourceView(project, id, query, signal)) as LycaonClient["locateSourceView"],
  };
  const explicit = Object.fromEntries(Object.entries(overrides).filter(([key, value]) =>
    key !== "openEditorDocument" && (typeof value !== "function" || !fixtureMethods.has(value))));
  Object.values(methods).forEach(method => fixtureMethods.add(method));
  const stubs: ClientStubs = { ...methods, ...explicit, ...(tree.getSourceWorkspace ? { getSourceWorkspace: tree.getSourceWorkspace } : {}) };
  // Without a host opener the refusal model stays callable but does not travel with a spread,
  // so a client spread over another keeps that client's document host.
  Object.defineProperty(stubs, "openEditorDocument", { value: openEditorDocument, enumerable: hostDocument !== undefined, writable: true, configurable: true });
  const client = stubClient(stubs);
  return client;
}

function comparisonTarget(source: SourceComparisonSelector): SourceComparisonTarget {
  switch (source.kind) {
    case "effect": return { effectId: source.effect_id };
    case "version": return { versionId: source.version_id };
    case "blob": return { blobOid: source.blob_oid, rootId: source.root_id, beforeBlobOid: source.before_blob_oid, displayPath: source.display_path };
    case "reviewed": return { fileId: source.file_id, reviewedThroughOrdinal: source.reviewed_through_ordinal };
    case "turn": return { fileId: source.file_id, sessionId: source.session_id, turn: source.turn, ...(source.mark_user_edits === undefined ? {} : { markUserEdits: source.mark_user_edits }) };
    case "scope": return { fileId: source.file_id, baseline: source.baseline, ...(source.mark_user_edits === undefined ? {} : { markUserEdits: source.mark_user_edits }), ...(source.presentation_after_ordinal === undefined ? {} : { presentationAfterOrdinal: source.presentation_after_ordinal }) };
    case "commit": return { rootId: source.root_id, path: source.path, baseline: "commit", expectedHead: source.expected_head };
    default: throw new Error(`Unsupported comparison fixture selector: ${source.kind}`);
  }
}
