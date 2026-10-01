export type BlueprintReviewView = "preview" | "code";

const LEADING_FRONTMATTER_RE = /^---\r?\n[\s\S]*?\r?\n---\r?\n?/;

/** Removes frontmatter already represented by compact card chrome. */
export function blueprintPreviewSource(content: string): string {
  return content.replace(LEADING_FRONTMATTER_RE, "");
}

const LEADING_TITLE_HEADING_RE = /^\s*#(?!#)[^\n]*\n?/;

/** True for a stub: frontmatter and at most a title heading, no body. */
export function blueprintIsTitleOnly(content: string): boolean {
  return (
    blueprintPreviewSource(content)
      .replace(LEADING_TITLE_HEADING_RE, "")
      .trim() === ""
  );
}

/** A new blueprint revision makes a new workspace offer for the same run. */
export function blueprintWorkspaceOfferKey(
  runId: string,
  revisionKey?: string | null,
): string {
  return `${runId.trim()}:${revisionKey?.trim() || "0"}`;
}

/** Tracks workspace offers across ChatView remounts. */
const offered = new Set<string>();

export function blueprintWorkspaceOffered(key: string): boolean {
  return offered.has(key);
}

export function markBlueprintWorkspaceOffered(key: string): void {
  offered.add(key);
}

export function resetBlueprintWorkspaceOffersForTests(): void {
  offered.clear();
}
