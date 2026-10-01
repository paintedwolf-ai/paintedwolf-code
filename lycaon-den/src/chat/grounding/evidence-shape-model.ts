import type { CitationGroundingEvidenceRecord } from "../../api/types.ts";

export type EvidenceRecordView = {
  handle?: string;
  kind?: string;
  shape?: string;
  fidelity?: string;
  tool?: string;
  path?: string;
  urls: string[];
  line?: number;
  excerpt?: string;
  truncated?: boolean;
};

export function mapEvidenceRecord(
  rec: CitationGroundingEvidenceRecord,
): EvidenceRecordView {
  return {
    handle: rec.handle?.trim() || undefined,
    kind: rec.kind?.trim() || undefined,
    shape: rec.shape?.trim() || undefined,
    fidelity: rec.fidelity?.trim() || undefined,
    tool: rec.tool?.trim() || undefined,
    path: rec.path?.trim() || undefined,
    urls: (rec.urls ?? []).map((u) => u.trim()).filter(Boolean),
    line: rec.line,
    excerpt: rec.excerpt?.trim() || undefined,
    truncated: rec.truncated,
  };
}

export function evidenceShapeLabel(shape?: string): string {
  const id = shape?.trim();
  if (!id) return "Evidence";
  return id.replace(/_/g, " ");
}

export function fidelityLabel(tier?: string): string {
  switch (tier?.trim()) {
    case "structured":
      return "Structured";
    case "scraped":
      return "Scraped";
    case "opaque":
      return "Opaque — verbatim capture, no semantics claimed";
    default:
      return tier?.trim() || "Unknown trust";
  }
}

export function renderEvidenceRecordSummary(rec: EvidenceRecordView): string {
  const shape = rec.shape ?? "unknown";
  switch (shape) {
    case "file_region":
      if (rec.path) {
        return rec.line ? `${rec.path}:${rec.line}` : rec.path;
      }
      return rec.handle ?? "File region";
    case "url": {
      const [first, ...rest] = rec.urls;
      if (!first) return rec.handle ?? "URL";
      return rest.length ? `${first} +${rest.length} more` : first;
    }
    case "command":
    case "opaque":
      return rec.excerpt ?? rec.handle ?? "Captured output";
    case "artifact":
      return rec.path ?? rec.excerpt ?? rec.handle ?? "Artifact";
    default:
      return (
        [rec.kind, rec.path, rec.urls[0], rec.excerpt, rec.handle]
          .filter(Boolean)
          .join(" · ") || "Evidence record"
      );
  }
}

export function evidenceRecordDetailLines(rec: EvidenceRecordView): string[] {
  // The header already carries shape and trust.
  const lines: string[] = [];
  if (rec.tool) lines.push(`Tool: ${rec.tool}`);
  // URL records may capture multiple results.
  if (rec.shape === "url" && rec.urls.length > 1) {
    for (const url of rec.urls.slice(1)) lines.push(url);
  }
  if (rec.truncated) {
    lines.push("Capture truncated — open source for full output");
  }
  if (rec.excerpt && (rec.shape === "opaque" || rec.shape === "command")) {
    lines.push(`Excerpt: ${rec.excerpt}`);
  }
  return lines;
}
