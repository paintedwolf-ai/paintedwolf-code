import type { SourceReaderAccess } from "../../../api/source-reader.ts";
import type { FileEditLineStat } from "../../../chat/file-edit/file-edit-fold.ts";
import type { ResolveProjectRoot } from "../../../api/project-path.ts";
import type { ChatDestination } from "../../../chat/composer/shared-composer-document.ts";
import type { SourceReaderChange } from "../reader/source-reader-change.ts";

/** One selectable comparison inside a row; `null` selects the net range. */
export type DiffRowRevision = { key: string; label: string; ariaLabel: string };

/** A source rebuilt from the same writes lists the same revisions; the strip and the comparison stay put. */
export function sameDiffRowRevisions(
  before: readonly DiffRowRevision[] | undefined,
  after: readonly DiffRowRevision[],
): boolean {
  if (!before || before.length !== after.length) return false;
  return before.every((revision, index) => {
    const next = after[index]!;
    return revision.key === next.key && revision.label === next.label && revision.ariaLabel === next.ariaLabel;
  });
}

/** Presentation source for one file diff row. */
export type DiffRowSource = {
  /** Disclosure and accordion identity; stable across refreshes. */
  key: string;
  projectId?: string;
  sessionId?: string | null;
  /** Worker overlay; null selects the project tree. */
  jobId?: string | null;
  rootRefs?: readonly ResolveProjectRoot[];
  /** Null omits Add to chat; absent uses the surrounding chat scope. */
  chatDestination?: ChatDestination | null;
  /** Path the header links, for the selected revision. */
  path(revision: number | null): string;
  rootId(revision: number | null): string | undefined;
  /** False keeps the header plain, for a file absent from the tree. */
  openable(revision: number | null): boolean | undefined;
  change(revision: number | null): SourceReaderChange;
  /** Counts the host already supplied; null asks the comparison for them. */
  stat(revision: number | null): FileEditLineStat | null;
  /** Whether the revision has zero net line changes. */
  isNoop(revision: number | null): boolean;
  /** Whether the comparison involves binary content. */
  isBinary?(revision: number | null): boolean;
  /** Empty leaves the row with only its net range. */
  revisions: readonly DiffRowRevision[];
  /** The label the strip gives the net range. */
  netLabel: string;
  /** Undefined while the client is unavailable. */
  access(revision: number | null): SourceReaderAccess | undefined;
  /** Screen-reader name for the strip. */
  revisionsLabel: string;
  /** Note displayed when the diff has no text changes to show. */
  noopNote: string;
};
