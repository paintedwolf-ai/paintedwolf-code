/**
 * Path-file chip labels and exact-range identity for Add-to-chat.
 * En-dash in the visible label; wire/fence use ASCII `start-end`.
 */

export type PathFileLineRange = {
  startLine: number;
  endLine: number;
};

/** Inclusive 1-based range, or null when empty / invalid. */
export function normalizeLineRange(
  start?: number | null,
  end?: number | null,
): PathFileLineRange | null {
  if (start == null || !Number.isFinite(start) || start < 1) return null;
  const s = Math.floor(start);
  const e =
    end != null && Number.isFinite(end) && end >= 1
      ? Math.floor(end)
      : s;
  return s <= e ? { startLine: s, endLine: e } : { startLine: e, endLine: s };
}

/** Chip label: `basename:12–34` or `@root/basename:12–34` when multi-root. */
export function pathFileChipLabel(args: {
  path: string;
  name?: string;
  rootLabel?: string;
  multiRoot?: boolean;
  startLine?: number | null;
  endLine?: number | null;
}): string {
  const base =
    (args.name ?? "").trim() ||
    args.path.replace(/\\/g, "/").split("/").filter(Boolean).pop() ||
    args.path ||
    "file";
  const range = normalizeLineRange(args.startLine, args.endLine);
  let label = base;
  if (range) {
    label =
      range.startLine === range.endLine
        ? `${base}:${range.startLine}`
        : `${base}:${range.startLine}–${range.endLine}`;
  }
  const root = (args.rootLabel ?? "").trim();
  if (args.multiRoot && root) {
    return `@${root}/${label}`;
  }
  return label;
}

export function samePathFileRange(
  a: {
    projectId: string;
    rootId: string;
    path: string;
    startLine?: number | null;
    endLine?: number | null;
  },
  b: {
    projectId: string;
    rootId: string;
    path: string;
    startLine?: number | null;
    endLine?: number | null;
  },
): boolean {
  if (
    a.projectId !== b.projectId ||
    a.rootId !== b.rootId ||
    a.path !== b.path
  ) {
    return false;
  }
  const ar = normalizeLineRange(a.startLine, a.endLine);
  const br = normalizeLineRange(b.startLine, b.endLine);
  if (!ar && !br) return true;
  if (!ar || !br) return false;
  return ar.startLine === br.startLine && ar.endLine === br.endLine;
}

/** Verb templates, inserted only when the composer draft is empty. */
export const SELECTION_VERB_TEMPLATES = {
  explain: "Explain this.",
  improve: "Improve this.",
  addTest: "Add a test covering this.",
} as const;

export type SelectionVerb = keyof typeof SELECTION_VERB_TEMPLATES;
