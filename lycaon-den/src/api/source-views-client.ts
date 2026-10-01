import { formatQuery } from "./http.ts";
import type {
  SourceComparisonAnchor,
  SourceComparisonDigestRequest,
  SourceComparisonDigests,
  SourceTreeAddress,
  SourceTreeFramePrefix,
  SourceView,
  SourcePresentation,
  SourcePresentationCreate,
  SourceViewCreate,
  SourceViewFrame,
  SourceViewLocation,
  SourceViewSearchPage,
  SourceViewUpdate,
  SourceViewportInterest,
} from "./types.ts";

export type SourceViewAnchor = SourceTreeAddress | SourceComparisonAnchor;
export type SourceViewRowsQuery = {
  presentation_id: string;
  offset?: number;
  limit?: number;
  anchor?: SourceViewAnchor;
  context_before?: number;
  retain?: SourceTreeFramePrefix[];
};
export type SourceViewLocationQuery = { presentation_id: string } & SourceViewLocationTarget;
export type SourceViewLocationTarget = (
  | { anchor: SourceViewAnchor; line?: never; side?: never }
  | { line: number; side: "before" | "after"; anchor?: never });
export type SourceViewSearchQuery = { presentation_id: string; q: string; case_sensitive?: boolean; cursor?: string; limit?: number };

export interface SourceViewsClient {
  replaceSourceViewportInterest(project: string, view: string, interest: string, demand: SourceViewportInterest, signal?: AbortSignal): Promise<void>;
  releaseSourceViewportInterest(project: string, view: string, interest: string): Promise<void>;
  createSourcePresentation(projectId: string, viewId: string, request: SourcePresentationCreate, signal?: AbortSignal): Promise<SourcePresentation>;
  releaseSourcePresentation(projectId: string, viewId: string, presentationId: string): Promise<void>;
  createSourceView(projectId: string, request: SourceViewCreate, signal?: AbortSignal): Promise<SourceView>;
  getSourceView(projectId: string, viewId: string, signal?: AbortSignal): Promise<SourceView>;
  applySourceViewIntent(projectId: string, viewId: string, request: SourceViewUpdate): Promise<SourceView>;
  releaseSourceView(projectId: string, viewId: string): Promise<void>;
  getSourceViewRows(projectId: string, viewId: string, query: SourceViewRowsQuery, signal?: AbortSignal): Promise<SourceViewFrame>;
  locateSourceView(projectId: string, viewId: string, query: SourceViewLocationQuery, signal?: AbortSignal): Promise<SourceViewLocation>;
  searchSourceView(projectId: string, viewId: string, query: SourceViewSearchQuery, signal?: AbortSignal): Promise<SourceViewSearchPage>;
  digestSourceComparisons(projectId: string, request: SourceComparisonDigestRequest, signal?: AbortSignal): Promise<SourceComparisonDigests>;
}

type JsonRequest = <T>(path: string, init?: RequestInit) => Promise<T>;

export function encodeSourceViewAnchor(anchor: SourceViewAnchor): string {
  const bytes = new TextEncoder().encode(JSON.stringify(anchor));
  let binary = "";
  for (let at = 0; at < bytes.length; at += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(at, at + 0x8000));
  }
  const encoded = btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
  if (encoded.length > 131_072) throw new Error("The source address exceeds the navigation limit.");
  return encoded;
}

export function createSourceViewsClient(json: JsonRequest): SourceViewsClient {
  const collection = (project: string) => `/v1/projects/${encodeURIComponent(project)}/source/views`;
  const view = (project: string, id: string) => `${collection(project)}/${encodeURIComponent(id)}`;
  const body = (method: "POST" | "PUT", value: unknown): RequestInit => ({ method, body: JSON.stringify(value) });
  return {
    replaceSourceViewportInterest: (project, id, interest, demand, signal) => json(`${view(project, id)}/interests/${encodeURIComponent(interest)}`, { ...body("PUT", demand), signal }),
    releaseSourceViewportInterest: (project, id, interest) => json(`${view(project, id)}/interests/${encodeURIComponent(interest)}`, { method: "DELETE" }),
    createSourcePresentation: (project, id, request, signal) => json(`${view(project, id)}/presentations`, { ...body("POST", request), signal }),
    releaseSourcePresentation: (project, id, presentation) => json(`${view(project, id)}/presentations/${encodeURIComponent(presentation)}`, { method: "DELETE" }),
    createSourceView: (project, request, signal) => json(collection(project), { ...body("POST", request), signal }),
    getSourceView: (project, id, signal) => json(view(project, id), { signal }),
    applySourceViewIntent: (project, id, request) => json(`${view(project, id)}/apply`, body("POST", request)),
    releaseSourceView: (project, id) => json(view(project, id), { method: "DELETE" }),
    getSourceViewRows: (project, id, query, signal) => json(`${view(project, id)}/presentations/${encodeURIComponent(query.presentation_id)}/rows${formatQuery({
      ...query, presentation_id: undefined,
      retain: query.retain?.length ? JSON.stringify(query.retain) : undefined,
      anchor: query.anchor ? encodeSourceViewAnchor(query.anchor) : undefined,
    })}`, { signal }),
    locateSourceView: (project, id, query, signal) => json(`${view(project, id)}/presentations/${encodeURIComponent(query.presentation_id)}/locate${formatQuery({
      ...query, presentation_id: undefined,
      anchor: query.anchor ? encodeSourceViewAnchor(query.anchor) : undefined,
    })}`, { signal }),
    searchSourceView: (project, id, query, signal) => json(`${view(project, id)}/presentations/${encodeURIComponent(query.presentation_id)}/search${formatQuery({ ...query, presentation_id: undefined })}`, { signal }),
    digestSourceComparisons: (project, request, signal) => json(`/v1/projects/${encodeURIComponent(project)}/source/comparison-digests`, { ...body("POST", request), signal }),
  };
}
