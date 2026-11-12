import { Facet } from "@codemirror/state";
import type { DenEditorKeymap } from "../../../../shared/app-state-types.ts";

/** The editing style installed in an editor; null where none is. */
export const installedEditingStyle = Facet.define<DenEditorKeymap, DenEditorKeymap | null>({
  combine: (values) => values[0] ?? null,
});
