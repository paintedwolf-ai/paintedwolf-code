import type { LycaonClient } from "../../api/client.ts";
import type { SecurityFinding, SourceAttributionResponse } from "../../api/types.ts";

type AnnotationSource = {
  projectId: string;
  rootId: string;
  path: string;
  sessionId?: string;
};

export type EditorAnnotations = {
  attribution: SourceAttributionResponse;
  findings: SecurityFinding[];
  scanId: string;
};

/** Identical file bytes can have different attribution and findings. */
export async function loadEditorAnnotations(
  client: LycaonClient,
  source: AnnotationSource,
): Promise<EditorAnnotations> {
  const [attribution, scan] = await Promise.all([
    Promise.resolve().then(() => client.getProjectSourceAttribution(source.projectId, {
      rootId: source.rootId, path: source.path, sessionId: source.sessionId,
    })).catch((): SourceAttributionResponse => ({ head_sha256: "", intervals: [] })),
    loadScanFindings(client, source).catch(() => ({ scanId: "", findings: [] })),
  ]);
  return { attribution, ...scan };
}

async function loadScanFindings(client: LycaonClient, source: AnnotationSource) {
  const { scans } = await client.listCodeScans(source.projectId, { limit: 8 });
  const scanId = scans.find((scan) => scan.status === "complete")?.id ?? "";
  if (!scanId) return { scanId, findings: [] };
  const result = await client.queryCodeScan(source.projectId, scanId, { path: source.path, limit: 200 });
  return { scanId, findings: result.findings ?? [] };
}
