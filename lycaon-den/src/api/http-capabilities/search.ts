import { lycaonDownload, type JsonRequester, jsonRequest } from "../http.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import type { SearchExportResult } from "../search-export.ts";
import type {
  SearchResponse,
  SearchBudget,
  SearchExportFormat,
  SearchReplacePreviewRequest,
  SearchReplacePreviewResponse,
  SearchReplaceApplyRequest,
  SearchReplaceApplyResponse,
} from "../types.ts";

/** Code-match flags default to false. */
export type SearchMatchOptions = {
  regex?: boolean;
  caseSensitive?: boolean;
  wholeWord?: boolean;
  include?: string[];
  exclude?: string[];
  cursor?: string;
  limit?: number;
  /** Crossbar uses `interactive`; depth/export omit (complete). */
  budget?: SearchBudget;
};

export interface SearchClient {
  search(
    query: string,
    originProjectId?: string,
    match?: SearchMatchOptions,
    signal?: AbortSignal,
  ): Promise<SearchResponse>;
  previewSearchReplacement(
    req: SearchReplacePreviewRequest,
    signal?: AbortSignal,
  ): Promise<SearchReplacePreviewResponse>;
  applySearchReplacement(
    req: SearchReplaceApplyRequest,
  ): Promise<SearchReplaceApplyResponse>;
  exportSearchResults(
    query: string,
    format: SearchExportFormat,
    originProjectId?: string,
    match?: SearchMatchOptions,
  ): Promise<SearchExportResult>;
}

export function createSearchClient(j: JsonRequester, connection: BackendConnection): SearchClient {
  return {
    search: (query, originProjectId, match, signal) =>
      j("/v1/search", { ...jsonRequest("POST", {
        query,
        ...(originProjectId ? { origin_project_id: originProjectId } : {}),
        ...(match?.regex ? { regex: true } : {}),
        ...(match?.caseSensitive ? { case_sensitive: true } : {}),
        ...(match?.wholeWord ? { whole_word: true } : {}),
        ...(match?.include?.length ? { include: match.include } : {}),
        ...(match?.exclude?.length ? { exclude: match.exclude } : {}),
        ...(match?.cursor ? { cursor: match.cursor } : {}),
        ...(match?.limit ? { limit: match.limit } : {}),
        ...(match?.budget ? { budget: match.budget } : {}),
      }), signal }),

    previewSearchReplacement: (req, signal) =>
      j("/v1/search/replace/preview", { ...jsonRequest("POST", req), signal }),

    applySearchReplacement: (req) =>
      j("/v1/search/replace/apply", jsonRequest("POST", req)),

    exportSearchResults: async (query, format, originProjectId, match) => {
      const download = await lycaonDownload(
        connection,
        "/v1/search/export",
        `search-export.${format}`,
        jsonRequest("POST", {
          query,
          format,
          ...(originProjectId ? { origin_project_id: originProjectId } : {}),
          ...(match?.regex ? { regex: true } : {}),
          ...(match?.caseSensitive ? { case_sensitive: true } : {}),
          ...(match?.wholeWord ? { whole_word: true } : {}),
          ...(match?.include?.length ? { include: match.include } : {}),
          ...(match?.exclude?.length ? { exclude: match.exclude } : {}),
        }),
      );
      return {
        blob: download.blob,
        truncated: download.headers.get("X-Export-Truncated") === "true",
        filename: download.filename,
      };
    },

  };
}
