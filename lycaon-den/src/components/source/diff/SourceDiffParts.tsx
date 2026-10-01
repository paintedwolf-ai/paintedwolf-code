import { DenButton } from "../../primitives/DenButton.tsx";
import { cn } from "../../../shared/cn.ts";
import { languageLabelForPath } from "../editor/codemirror-lang.ts";
import type { FileEditLineStat } from "../../../chat/file-edit/file-edit-fold.ts";

export function LineStat(props: { stat: FileEditLineStat }) {
  return (
    <span
      class="den-file-edit-diff-stat"
      data-testid="file-edit-diff-stat"
      aria-label={`${props.stat.added} added, ${props.stat.removed} removed`}
    >
      <span class="den-file-edit-diff-stat-add">+{props.stat.added}</span>
      <span class="den-file-edit-diff-stat-del">−{props.stat.removed}</span>
    </span>
  );
}

function lineCount(count: number, verb: "added" | "removed"): string {
  return `${count} ${count === 1 ? "line" : "lines"} ${verb}`;
}

/** Stands in for the body of a whole added or deleted file. */
export function WholeFileDetails(props: {
  change: "added" | "deleted";
  path: string;
  stat: FileEditLineStat | null | undefined;
  onView: () => void;
  class?: string;
}) {
  const added = () => props.change === "added";
  return (
    <div class={cn("den-file-edit-diff-details", props.class)} data-testid="file-edit-diff-details">
      <div class="den-file-edit-diff-details-header">
        <span class="den-file-edit-diff-details-badge" data-change={props.change}>
          {added() ? "New file" : "Deleted file"}
        </span>
        <span class="den-file-edit-diff-details-lang">{languageLabelForPath(props.path)}</span>
      </div>
      <span class="den-file-edit-diff-details-lines">
        {added() ? lineCount(props.stat?.added ?? 0, "added") : lineCount(props.stat?.removed ?? 0, "removed")}
      </span>
      <DenButton variant="secondary" compact class="den-file-edit-diff-details-btn" onClick={() => props.onView()}>
        {added() ? "View file" : "View removed contents"}
      </DenButton>
    </div>
  );
}
