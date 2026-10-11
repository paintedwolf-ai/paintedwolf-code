import { createSignal } from "solid-js";
import type { SearchExportFormat } from "../../api/types.ts";
import type { SearchMatchOptions } from "../../api/http-capabilities/search.ts";
import { LycaonApiError } from "../../api/http.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { lintFromApiError, type QueryLintError } from "../../search/search-query-model.ts";
import { downloadExport } from "../../platform/files/save-file.ts";
type Options = {
 query: () => string; originProjectId: () => string | null;
 matchOptions: () => SearchMatchOptions; apiLint: () => QueryLintError | null;
 setApiLint: (lint: QueryLintError | null) => void; reportError: (error: unknown) => void;
};
export function createSearchExport(options: Options) {
 const [exportTruncated, setExportTruncated] = createSignal(false);
  const runExport = async (format: SearchExportFormat) => {
    const trimmed = options.query().trim();
    if (!trimmed) return;
    const client = getLycaonClient();
    if (!client) return;
    setExportTruncated(false);
    try {
      // Live search flags, so the export matches the screen.
      const result = await client.exportSearchResults(
        trimmed,
        format,
        options.originProjectId() ?? undefined,
        options.matchOptions(),
      );
      await downloadExport(result.blob, result.filename);
      setExportTruncated(result.truncated);
    } catch (err) {
      if (
        err instanceof LycaonApiError &&
        (err.code === "search_query_invalid" ||
          err.code === "search_pattern_invalid")
      ) {
        options.setApiLint(lintFromApiError(err));
        return;
      }
      options.reportError(err);
    }
  };

  const exportDisabled = () => !options.query().trim() || !!options.apiLint();
 return { exportTruncated, runExport, exportDisabled };
}
