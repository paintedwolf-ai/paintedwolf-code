import { getIndentUnit, syntaxTree } from "@codemirror/language";
import { type Annotation, type EditorState, type Extension, StateEffect, StateField } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { keepPositionInView } from "../editor/editor-settled-scroll.ts";
import {
  awaitsParser,
  buildLayout,
  extendLayout,
  NO_WORK,
  OFF,
  type PrettyConfig,
  type PrettyLayoutState,
  type PrettyOutcome,
  type PrettyWork,
  updateLayout,
} from "./pretty-rescan.ts";
import { prettyTabMarks } from "./pretty-tab-marks.ts";
import { prettyLayoutActive, prettyLayoutDecorations, prettyPresentation } from "./pretty-view.ts";

const setPrettyConfig = StateEffect.define<PrettyConfig>();

function sameConfig(a: PrettyConfig, b: PrettyConfig): boolean {
  return a.enabled === b.enabled && a.path === b.path && a.languageHint === b.languageHint;
}

export const prettyLayoutField = StateField.define<PrettyLayoutState>({
  create: (state) => buildLayout(state, { enabled: false }),
  update(prev, tr) {
    let config = prev.config;
    for (const effect of tr.effects) if (effect.is(setPrettyConfig)) config = effect.value;
    const state = tr.state;
    if (!sameConfig(config, prev.config) || getIndentUnit(state) !== prev.indentUnit || state.tabSize !== prev.tabSize) {
      return buildLayout(state, config);
    }
    if (prev.outcome.kind === "off" || prev.outcome.kind === "unavailable") return prev;
    if (tr.docChanged) return updateLayout(prev, tr);
    // The parser reached further without an edit.
    if (syntaxTree(state) !== syntaxTree(tr.startState) && awaitsParser(prev, state)) return extendLayout(prev, state);
    return prev;
  },
  provide: (field) => [
    EditorView.decorations.from(field, (state) => state.decorations),
    prettyLayoutDecorations.from(field, (state) => state.decorations),
    prettyLayoutActive.from(field, (state) => state.outcome.kind === "active"),
  ],
});

export function prettyOutcomeOf(state: EditorState): PrettyOutcome {
  return state.field(prettyLayoutField, false)?.outcome ?? OFF;
}

export function prettyWorkOf(state: EditorState): PrettyWork {
  return state.field(prettyLayoutField, false)?.work ?? NO_WORK;
}

export function createPrettyExtension(initial: PrettyConfig = { enabled: false }): Extension {
  return [prettyPresentation, prettyTabMarks, prettyLayoutField.init((state) => buildLayout(state, initial))];
}

/** Applies a configuration; turning layout on or off keeps the selection head in view. */
export function applyPrettyConfig(
  view: EditorView,
  config: PrettyConfig,
  annotations?: readonly Annotation<unknown>[],
): void {
  const current = view.state.field(prettyLayoutField, false);
  if (current && sameConfig(current.config, config)) return;
  const toggled = current !== undefined && current.config.enabled !== config.enabled;
  const head = (state: EditorState) => state.selection.main.head;
  view.dispatch({
    effects: [
      setPrettyConfig.of(config),
      ...(toggled ? [EditorView.scrollIntoView(head(view.state), { y: "nearest" })] : []),
    ],
    ...(annotations ? { annotations: [...annotations] } : {}),
  });
  // The reflow moves the selection head; it stays in view.
  if (toggled) keepPositionInView(view, head);
}
