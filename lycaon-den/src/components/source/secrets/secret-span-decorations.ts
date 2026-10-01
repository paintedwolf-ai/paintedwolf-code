import {
  Facet,
  RangeSetBuilder,
  StateEffect,
  StateField,
  type EditorState,
  type Extension,
} from "@codemirror/state";
import { Decoration, EditorView } from "@codemirror/view";
import { secretSpanMarks, type SecretSpanMark } from "./secret-span-model.ts";
import type { SecretScreen, SecretSpanState } from "../../../api/types.ts";

const setScreen = StateEffect.define<SecretScreen | null>();

const EMPTY: readonly SecretSpanMark[] = [];

/** Host spans projected into a read-only document's displayed coordinates. */
export const projectedSecretSpans = Facet.define<readonly SecretSpanMark[], readonly SecretSpanMark[]>({
  combine: sources => sources.flat(),
});

/** Maps screened spans through edits until the next screen arrives. */
const secretSpanField = StateField.define<readonly SecretSpanMark[]>({
  create: () => EMPTY,
  update(value, tr) {
    for (const effect of tr.effects) {
      if (effect.is(setScreen)) return secretSpanMarks(effect.value, tr.state.doc);
    }
    if (!tr.docChanged || value.length === 0) return value;
    const mapped: SecretSpanMark[] = [];
    for (const mark of value) {
      const from = tr.changes.mapPos(mark.from, 1);
      const to = tr.changes.mapPos(mark.to, -1);
      if (to > from) mapped.push({ ...mark, from, to });
    }
    return mapped;
  },
});

export const SECRET_SPAN_CLASSES = {
  tracked: "cm-den-secret cm-den-secret--tracked",
  retired: "cm-den-secret cm-den-secret--retired",
  detected: "cm-den-secret cm-den-secret--detected",
} satisfies Record<SecretSpanState, string>;

const MARK_BY_STATE = {
  tracked: Decoration.mark({ class: SECRET_SPAN_CLASSES.tracked }),
  retired: Decoration.mark({ class: SECRET_SPAN_CLASSES.retired }),
  detected: Decoration.mark({ class: SECRET_SPAN_CLASSES.detected }),
} as const;

const decorations = EditorView.decorations.compute([secretSpanField], (state) => {
  const marks = state.field(secretSpanField);
  if (marks.length === 0) return Decoration.none;
  const builder = new RangeSetBuilder<Decoration>();
  for (const mark of marks) {
    if (mark.from >= mark.to || mark.to > state.doc.length) continue;
    builder.add(mark.from, mark.to, MARK_BY_STATE[mark.state]);
  }
  return builder.finish();
});

const theme = EditorView.baseTheme({
  ".cm-den-secret": {
    textDecorationLine: "underline",
    textUnderlineOffset: "3px",
    borderRadius: "1px",
  },
  ".cm-den-secret--tracked": {
    backgroundColor: "color-mix(in srgb, var(--den-status-positive) 12%, transparent)",
    textDecorationStyle: "solid",
    textDecorationThickness: "2px",
    textDecorationColor: "var(--den-status-positive)",
  },
  ".cm-den-secret--detected": {
    backgroundColor: "color-mix(in srgb, var(--den-warning) 12%, transparent)",
    textDecorationStyle: "dashed",
    textDecorationThickness: "2px",
    textDecorationColor: "var(--den-warning)",
  },
  ".cm-den-secret--retired": {
    backgroundColor: "var(--den-surface-inset)",
    textDecorationStyle: "wavy",
    textDecorationThickness: "1px",
    textDecorationColor: "var(--den-text-muted)",
  },
});

export const secretSpanExtension: Extension = [secretSpanField, decorations, theme];

export function setSecretScreen(view: EditorView, screen: SecretScreen | null): void {
  view.dispatch({ effects: setScreen.of(screen) });
}

function secretSpanMarksIn(state: EditorState): readonly SecretSpanMark[] {
  return [...(state.field(secretSpanField, false) ?? EMPTY), ...state.facet(projectedSecretSpans)];
}

export function getSecretSpanMarks(view: EditorView): readonly SecretSpanMark[] {
  return secretSpanMarksIn(view.state);
}
