import type { FileEditPreview } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

/** The call's recorded file edits that name a path, in recorded order. */
export function fileEditsFromPart(part: ToolPartView): FileEditPreview[] {
  return (part.fileEdits ?? []).filter((edit) => edit.path.trim() !== "");
}

/** Whether the call changed file text: a recorded edit, or one past its result's bound. */
export function partHasFileEdits(part: ToolPartView): boolean {
  return fileEditsFromPart(part).length > 0 || (part.fileEditsOmitted ?? 0) > 0;
}
