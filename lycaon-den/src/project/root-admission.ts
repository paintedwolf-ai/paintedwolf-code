import type { ProjectRoot } from "../api/types.ts";

type AdmissionCode = NonNullable<ProjectRoot["admission_code"]>;

const REASONS: Record<AdmissionCode, string> = {
  write_root_contains_control_plane:
    "This folder contains Painted Wolf Code's own data, so agents don't work here. You can still browse and edit it.",
  write_root_under_secret_store:
    "This folder holds protected data, such as keys, credentials or Painted Wolf Code's own data, so agents don't work here. You can still browse and edit it.",
};

/** Label for a root agents don't work in; the host decides admission. */
export const EDITOR_ONLY_LABEL = "Editor only";

/** Why agents don't work in root, or null when they do. */
export function editorOnlyReason(root: Pick<ProjectRoot, "admitted" | "admission_code">): string | null {
  if (root.admitted) return null;
  return root.admission_code ? REASONS[root.admission_code] : null;
}
