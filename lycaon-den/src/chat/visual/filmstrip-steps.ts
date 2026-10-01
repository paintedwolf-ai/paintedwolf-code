import { peelEvidenceHandleTag } from "../tool/tool-output-handle.ts";

/** One filmstrip step for Den: rail label + optional structured action line. */
export type FilmstripStep = {
  index: number;
  label: string;
  /** Short action/result line for the reading pane (frame 0 = navigate idle). */
  detail: string;
  evidenceHandle?: string;
};

type ToolFrameRow = {
  index?: number;
  caption?: string;
  evidence_handle?: string;
};

type CapturePayload = {
  frames?: ToolFrameRow[];
  action_results?: unknown[];
  caption?: string;
  final_url?: string;
  log?: string[];
};

/**
 * Build step metadata from capture_page tool text (evidence tag peeled).
 * Frame 0 is navigate/idle; frames[1..] align with action_results[0..].
 */
export function filmstripStepsFromToolOutput(
  output: string | null | undefined,
  frameCount: number,
  zipCaptions: readonly string[] = [],
): FilmstripStep[] {
  const n = Math.max(0, frameCount);
  if (n === 0) return [];
  const body = peelEvidenceHandleTag(output ?? "").body.trim();
  let payload: CapturePayload = {};
  if (body.startsWith("{")) {
    try {
      payload = JSON.parse(body) as CapturePayload;
    } catch {
      payload = {};
    }
  }
  const rows = Array.isArray(payload.frames) ? payload.frames : [];
  const actions = Array.isArray(payload.action_results)
    ? payload.action_results
    : [];
  const out: FilmstripStep[] = [];
  for (let i = 0; i < n; i++) {
    const row = rows.find((r) => Number(r?.index) === i) ?? rows[i];
    const zipCap = (zipCaptions[i] ?? "").trim();
    const rowCap = typeof row?.caption === "string" ? row.caption.trim() : "";
    const label =
      rowCap ||
      zipCap ||
      (i === 0 ? "initial" : `step ${i}`);
    let detail: string;
    if (i === 0) {
      detail = navigateDetail(payload);
    } else {
      detail = formatActionResult(actions[i - 1]) || `After: ${label}`;
    }
    const handle =
      typeof row?.evidence_handle === "string"
        ? row.evidence_handle.trim()
        : "";
    out.push({
      index: i,
      label,
      detail,
      evidenceHandle: handle || undefined,
    });
  }
  return out;
}

function navigateDetail(payload: CapturePayload): string {
  const url =
    typeof payload.final_url === "string" ? payload.final_url.trim() : "";
  if (url) return `Navigate idle · ${url}`;
  return "Navigate idle";
}

function formatActionResult(raw: unknown): string {
  if (raw == null) return "";
  let obj: Record<string, unknown> | null = null;
  if (typeof raw === "string") {
    try {
      const parsed = JSON.parse(raw) as unknown;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        obj = parsed as Record<string, unknown>;
      } else {
        return raw.trim();
      }
    } catch {
      return raw.trim();
    }
  } else if (typeof raw === "object" && !Array.isArray(raw)) {
    obj = raw as Record<string, unknown>;
  }
  if (!obj) return "";
  const type = stringField(obj, "type");
  const ok = obj.ok;
  const target =
    stringField(obj, "selector") ||
    stringField(obj, "testid") ||
    stringField(obj, "label") ||
    stringField(obj, "text") ||
    stringField(obj, "role");
  const parts: string[] = [];
  if (type) parts.push(type);
  if (target) parts.push(target);
  if (ok === false) parts.push("failed");
  else if (ok === true && parts.length === 0) parts.push("ok");
  if (parts.length > 0) return parts.join(" · ");
  try {
    return JSON.stringify(obj);
  } catch {
    return "";
  }
}

function stringField(obj: Record<string, unknown>, key: string): string {
  const v = obj[key];
  return typeof v === "string" && v.trim() ? v.trim() : "";
}
