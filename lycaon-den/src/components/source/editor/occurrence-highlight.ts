import { RangeSetBuilder } from "@codemirror/state";
import {
  Decoration,
  ViewPlugin,
  type DecorationSet,
  type EditorView,
  type ViewUpdate,
} from "@codemirror/view";
import {
  collectOccurrenceHits,
  OCCURRENCE_MAX_MARKS,
  occurrenceTargetAt,
  occurrenceTargetKey,
  type OccurrenceHit,
} from "./occurrence-scan.ts";

const occurrenceMark = Decoration.mark({ class: "cm-den-occurrence" });
const selectionOccurrenceMark = Decoration.mark({ class: "cm-selectionMatch" });

function buildOccurrences(
  view: EditorView,
  target: ReturnType<typeof occurrenceTargetAt>,
  selectionHits: readonly OccurrenceHit[],
): DecorationSet {
  if (!target) return Decoration.none;
  const hits: OccurrenceHit[] = [];
  if (target.wholeWord) {
    for (const { from, to } of view.visibleRanges) {
      if (hits.length >= OCCURRENCE_MAX_MARKS) break;
      hits.push(
        ...collectOccurrenceHits(
          view.state.doc,
          target,
          {
            from,
            to,
            cap: OCCURRENCE_MAX_MARKS - hits.length,
          },
        ),
      );
    }
  } else {
    hits.push(...selectionHits);
  }
  if (hits.length === 0) return Decoration.none;
  const builder = new RangeSetBuilder<Decoration>();
  const mark = target.wholeWord ? occurrenceMark : selectionOccurrenceMark;
  for (const hit of hits) {
    builder.add(hit.from, hit.to, mark);
  }
  return builder.finish();
}

function visibleRangeKey(view: EditorView): string {
  let key = "";
  for (const { from, to } of view.visibleRanges) {
    key += `${from}:${to};`;
  }
  return key;
}

export const occurrenceHighlight = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    private targetKey = "";
    private rangeKey = "";
    private selectionHits: OccurrenceHit[] = [];

    constructor(view: EditorView) {
      const target = occurrenceTargetAt(view.state);
      this.targetKey = occurrenceTargetKey(target);
      this.rangeKey = visibleRangeKey(view);
      if (target && !target.wholeWord) {
        this.selectionHits = collectOccurrenceHits(view.state.doc, target, {
          cap: OCCURRENCE_MAX_MARKS,
        });
      }
      this.decorations = buildOccurrences(view, target, this.selectionHits);
    }

    update(update: ViewUpdate) {
      if (!(update.docChanged || update.selectionSet || update.viewportChanged)) {
        return;
      }
      const target = occurrenceTargetAt(update.view.state);
      const nextTargetKey = occurrenceTargetKey(target);
      const rangeKey = visibleRangeKey(update.view);
      const targetChanged = update.docChanged || nextTargetKey !== this.targetKey;
      if (
        !targetChanged &&
        (!target?.wholeWord || rangeKey === this.rangeKey)
      ) {
        return;
      }
      if (targetChanged) {
        this.selectionHits =
          target && !target.wholeWord
            ? collectOccurrenceHits(update.state.doc, target, {
                cap: OCCURRENCE_MAX_MARKS,
              })
            : [];
      }
      this.decorations = buildOccurrences(
        update.view,
        target,
        this.selectionHits,
      );
      this.targetKey = nextTargetKey;
      this.rangeKey = rangeKey;
    }
  },
  { decorations: (v) => v.decorations },
);
