import type { FileEditPreview } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

/** Keys omitted from tool card facts when a visual diff is shown. */
export const FILE_EDIT_ARG_KEYS = new Set([
  "content",
  "old_string",
  "new_string",
  "patch",
]);

/** Wire file_edit when the path is set. */
export function fileEditFromPart(part: ToolPartView): FileEditPreview | null {
  const edit = part.fileEdit;
  if (!edit?.path.trim()) return null;
  return edit;
}
