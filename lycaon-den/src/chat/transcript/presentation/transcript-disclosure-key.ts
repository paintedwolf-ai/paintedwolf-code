declare const transcriptDisclosureKeyBrand: unique symbol;

/**
 * Identity of one disclosure in a transcript's shared open state. Each surface has its own
 * namespace: a write's tool row and its file in the turn's diff group come from the same
 * record, and their open state must stay independent.
 */
export type TranscriptDisclosureKey = string & {
  readonly [transcriptDisclosureKeyBrand]: true;
};

const SURFACES = [
  "tool",
  "tool-network",
  "grounding-evidence",
  "activity-span",
  "index-warming",
  "diff-group",
  "diff-file",
  "checkpoint",
  "workflow-feedback",
  "workflow-explain",
  "blueprint",
  "draft",
] as const;

type TranscriptDisclosureSurface = (typeof SURFACES)[number];

const SURFACE_SET: ReadonlySet<string> = new Set(SURFACES);

function disclosureKey(
  surface: TranscriptDisclosureSurface,
  id: string,
): TranscriptDisclosureKey {
  const subject = id.trim();
  if (!subject) throw new Error(`${surface} disclosure needs an id`);
  return `${surface}:${subject}` as TranscriptDisclosureKey;
}

export const transcriptDisclosureKey = {
  tool: (partId: string) => disclosureKey("tool", partId),
  toolNetwork: (partId: string) => disclosureKey("tool-network", partId),
  groundingEvidence: (rowKey: string) => disclosureKey("grounding-evidence", rowKey),
  activitySpan: (leadEntryKey: string) => disclosureKey("activity-span", leadEntryKey),
  indexWarming: (entryKey: string) => disclosureKey("index-warming", entryKey),
  diffGroup: (entryKey: string) => disclosureKey("diff-group", entryKey),
  diffFile: (foldKey: string) => disclosureKey("diff-file", foldKey),
  checkpoint: (entryKey: string) => disclosureKey("checkpoint", entryKey),
  workflowFeedback: (entryKey: string) => disclosureKey("workflow-feedback", entryKey),
  workflowExplain: (entryKey: string) => disclosureKey("workflow-explain", entryKey),
  blueprint: (entryKey: string) => disclosureKey("blueprint", entryKey),
  draft: (slotId: string) => disclosureKey("draft", slotId),
} as const;

/** Reads a key back from markup or saved state; anything no surface wrote is dropped. */
export function parseTranscriptDisclosureKey(
  value: string | null | undefined,
): TranscriptDisclosureKey | undefined {
  const text = value?.trim() ?? "";
  const separator = text.indexOf(":");
  if (separator <= 0 || separator === text.length - 1) return undefined;
  return SURFACE_SET.has(text.slice(0, separator))
    ? (text as TranscriptDisclosureKey)
    : undefined;
}
