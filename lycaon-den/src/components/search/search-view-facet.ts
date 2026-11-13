import type { Accessor } from "solid-js";
import type { SearchHit } from "../../api/types.ts";
import { offerViewFacet, type ViewFacet } from "../../shell/view-report.ts";

const MAX_QUERY_BYTES = 1024;

/** The search a project's stage shows, with its selected hit when that hit is in the project. */
export function searchViewFacet(projectId: string | undefined, query: string, hit: SearchHit | null | undefined): ViewFacet {
  const text = query.trim();
  if (!text || new TextEncoder().encode(text).length > MAX_QUERY_BYTES) return {};
  const located = hit && hit.project_id === projectId && hit.root_id && hit.path;
  return {
    search: {
      query: text,
      hit: located ? { root_id: hit.root_id!, path: hit.path!, ...(hit.line ? { line: hit.line } : {}) } : undefined,
    },
  };
}

export function offerSearchViewFacet(deps: {
  projectId: Accessor<string | undefined>;
  query: Accessor<string>;
  selectedHit: Accessor<SearchHit | null | undefined>;
}): void {
  offerViewFacet(deps.projectId, () => searchViewFacet(deps.projectId(), deps.query(), deps.selectedHit()));
}
