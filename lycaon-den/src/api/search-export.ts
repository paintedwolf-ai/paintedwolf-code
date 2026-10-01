/** Client-local search export result (blob + response headers — not an OpenAPI JSON schema). */
export type SearchExportResult = {
  blob: Blob;
  truncated: boolean;
  filename: string;
};
