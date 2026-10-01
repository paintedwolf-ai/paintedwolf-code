import { Facet } from "@codemirror/state";

// Default text size multiplied by its line-height ratio.
const DEFAULT_EDITOR_LINE_HEIGHT_PX = 19.5;

// Block widgets reserve their height before DOM measurement.
export const editorLineHeightPx = Facet.define<number, number>({
  combine: (values) => values[0] ?? DEFAULT_EDITOR_LINE_HEIGHT_PX,
});
