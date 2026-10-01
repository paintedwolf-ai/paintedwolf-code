import { EVIDENCE_HANDLE_PATTERN } from "../host-markers.generated.ts";

const EVIDENCE_HANDLE_TAG_RE = new RegExp(`^\\[${EVIDENCE_HANDLE_PATTERN}\\]\\n?`);

export type PeeledToolOutput = {
  handle: string | null;
  body: string;
};

/** Separates the leading evidence handle from tool output. */
export function peelEvidenceHandleTag(content: string): PeeledToolOutput {
  const match = EVIDENCE_HANDLE_TAG_RE.exec(content);
  if (!match) {
    return { handle: null, body: content };
  }
  const handleMatch = /^\[(.+)\]/.exec(match[0].trimEnd());
  return {
    handle: handleMatch?.[1]?.trim() ?? null,
    body: content.slice(match[0].length),
  };
}
