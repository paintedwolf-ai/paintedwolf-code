import { EditorSelection, type SelectionRange } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { SearchQuery, setSearchQuery } from "@codemirror/search";
import {
  FIND_MATCH_COUNT_CAP,
  FIND_SELECT_ALL_CURSOR_CAP,
  type FindMatchCount,
  type FindProvider,
} from "./find-provider.ts";

export type CodeMirrorFindProviderOptions = {
  getView: () => EditorView | null | undefined;
};

type FrozenScope = { from: number; to: number };

export function createCodeMirrorFindProvider(
  opts: CodeMirrorFindProviderOptions,
): FindProvider {
  let queryText = "";
  let caseSensitive = false;
  let scope: FrozenScope | null = null;
  let activeIndex = -1;
  let lastCount: FindMatchCount = {
    total: 0,
    activeIndex: -1,
    capped: false,
  };

  function view(): EditorView | null {
    return opts.getView() ?? null;
  }

  function buildQuery(replace = ""): SearchQuery {
    return new SearchQuery({
      search: queryText,
      caseSensitive,
      literal: true,
      regexp: false,
      wholeWord: false,
      replace,
    });
  }

  function scopeBounds(v: EditorView): { from: number; to: number } {
    if (scope) {
      const docLen = v.state.doc.length;
      return {
        from: Math.max(0, Math.min(scope.from, docLen)),
        to: Math.max(0, Math.min(scope.to, docLen)),
      };
    }
    return { from: 0, to: v.state.doc.length };
  }

  function collectMatches(
    v: EditorView,
    cap = FIND_MATCH_COUNT_CAP,
  ): { matches: { from: number; to: number }[]; capped: boolean } {
    const q = buildQuery();
    if (!q.valid || !queryText) return { matches: [], capped: false };
    const { from, to } = scopeBounds(v);
    const matches: { from: number; to: number }[] = [];
    const cursor = q.getCursor(v.state, from, to);
    let capped = false;
    for (;;) {
      const next = cursor.next();
      if (next.done) break;
      matches.push({ from: next.value.from, to: next.value.to });
      if (matches.length >= cap) {
        capped = true;
        break;
      }
    }
    return { matches, capped };
  }

  function syncSearchState(v: EditorView, replace = ""): void {
    const q = buildQuery(replace);
    v.dispatch({ effects: setSearchQuery.of(q) });
  }

  function applyMatch(
    v: EditorView,
    match: { from: number; to: number },
    index: number,
  ): void {
    activeIndex = index;
    v.dispatch({
      selection: EditorSelection.range(match.from, match.to),
      effects: EditorView.scrollIntoView(match.from, { y: "center" }),
      userEvent: "select.search",
    });
  }

  function recomputeActiveFromSelection(
    matches: { from: number; to: number }[],
    head: number,
  ): number {
    if (matches.length === 0) return -1;
    let idx = 0;
    for (let i = 0; i < matches.length; i++) {
      if (matches[i]!.from <= head) idx = i;
      else break;
    }
    return idx;
  }

  function refreshCount(): FindMatchCount {
    const v = view();
    if (!v || !queryText) {
      lastCount = { total: 0, activeIndex: -1, capped: false };
      activeIndex = -1;
      return lastCount;
    }
    const { matches, capped } = collectMatches(v);
    const head = v.state.selection.main.head;
    let idx = activeIndex;
    if (idx < 0 || idx >= matches.length) {
      idx = recomputeActiveFromSelection(matches, head);
    }
    activeIndex = idx;
    lastCount = {
      total: capped ? FIND_MATCH_COUNT_CAP : matches.length,
      activeIndex: idx,
      capped,
    };
    return lastCount;
  }

  return {
    setQuery(q, optsIn) {
      queryText = q;
      caseSensitive = optsIn.caseSensitive;
      activeIndex = -1;
      const v = view();
      if (v) syncSearchState(v);
      refreshCount();
      // Land on the first match after the caret when the query is non-empty.
      if (v && queryText) {
        const { matches } = collectMatches(v);
        if (matches.length === 0) return;
        const head = v.state.selection.main.head;
        let idx = matches.findIndex((m) => m.from >= head);
        if (idx < 0) idx = 0;
        applyMatch(v, matches[idx]!, idx);
        refreshCount();
      }
    },

    count() {
      return refreshCount();
    },

    next() {
      const v = view();
      if (!v || !queryText) return;
      syncSearchState(v);
      const { matches } = collectMatches(v);
      if (matches.length === 0) {
        activeIndex = -1;
        refreshCount();
        return;
      }
      const head = v.state.selection.main.head;
      let idx = matches.findIndex((m) => m.from > head);
      if (idx < 0) idx = 0;
      // If the current selection already is this match, advance.
      const main = v.state.selection.main;
      const at = matches.findIndex(
        (m) => m.from === main.from && m.to === main.to,
      );
      if (at >= 0) idx = (at + 1) % matches.length;
      applyMatch(v, matches[idx]!, idx);
      refreshCount();
    },

    prev() {
      const v = view();
      if (!v || !queryText) return;
      syncSearchState(v);
      const { matches } = collectMatches(v);
      if (matches.length === 0) {
        activeIndex = -1;
        refreshCount();
        return;
      }
      const main = v.state.selection.main;
      const at = matches.findIndex(
        (m) => m.from === main.from && m.to === main.to,
      );
      let idx: number;
      if (at >= 0) idx = (at - 1 + matches.length) % matches.length;
      else {
        idx = -1;
        for (let i = matches.length - 1; i >= 0; i--) {
          if (matches[i]!.to <= main.head) {
            idx = i;
            break;
          }
        }
        if (idx < 0) idx = matches.length - 1;
      }
      applyMatch(v, matches[idx]!, idx);
      refreshCount();
    },

    replaceCurrent(text) {
      const v = view();
      if (!v || v.state.readOnly || !queryText) return false;
      const { matches } = collectMatches(v);
      if (matches.length === 0) return false;
      const main = v.state.selection.main;
      let idx = matches.findIndex(
        (m) => m.from === main.from && m.to === main.to,
      );
      if (idx < 0) idx = recomputeActiveFromSelection(matches, main.head);
      if (idx < 0) return false;
      const match = matches[idx]!;
      const replacementEnd = match.from + text.length;
      v.dispatch({
        changes: { from: match.from, to: match.to, insert: text },
        selection: EditorSelection.cursor(match.from + text.length),
        userEvent: "input.replace",
      });
      // Recompute and advance to the next remaining match.
      const after = collectMatches(v).matches;
      if (after.length === 0) {
        activeIndex = -1;
        refreshCount();
        return true;
      }
      let land = after.findIndex((candidate) => candidate.from >= replacementEnd);
      if (land < 0) {
        land = after.findIndex((candidate) => candidate.to <= match.from);
      }
      if (land < 0) land = 0;
      applyMatch(v, after[land]!, land);
      refreshCount();
      return true;
    },

    replaceAll(text) {
      const v = view();
      if (!v || v.state.readOnly || !queryText) {
        return { replaced: 0, capped: false };
      }
      const { matches, capped } = collectMatches(v);
      if (capped) return { replaced: 0, capped: true };
      if (matches.length === 0) return { replaced: 0, capped: false };
      // Replacements use the frozen scope.
      const changes = matches.map((m) => ({
        from: m.from,
        to: m.to,
        insert: text,
      }));
      v.dispatch({
        changes,
        userEvent: "input.replace.all",
      });
      activeIndex = -1;
      syncSearchState(v);
      refreshCount();
      return { replaced: matches.length, capped: false };
    },

    scopeToSelection(on) {
      const v = view();
      if (!v) return false;
      if (!on) {
        scope = null;
        refreshCount();
        return true;
      }
      const main = v.state.selection.main;
      if (main.empty) return false;
      const fromLine = v.state.doc.lineAt(main.from).number;
      const toLine = v.state.doc.lineAt(main.to).number;
      if (toLine - fromLine < 1) return false;
      scope = {
        from: Math.min(main.from, main.to),
        to: Math.max(main.from, main.to),
      };
      refreshCount();
      return true;
    },

    supportsScope() {
      return true;
    },

    selectionSpansMultipleLines() {
      const v = view();
      if (!v) return false;
      const main = v.state.selection.main;
      if (main.empty) return false;
      return (
        v.state.doc.lineAt(main.to).number - v.state.doc.lineAt(main.from).number >=
        1
      );
    },

    selectAllMatches() {
      const v = view();
      if (!v || !queryText) return null;
      const { matches } = collectMatches(v, FIND_SELECT_ALL_CURSOR_CAP + 1);
      if (matches.length === 0) return { cursors: 0, capped: false };
      const capped = matches.length > FIND_SELECT_ALL_CURSOR_CAP;
      const take = matches.slice(0, FIND_SELECT_ALL_CURSOR_CAP);
      const ranges: SelectionRange[] = take.map((m) =>
        EditorSelection.range(m.from, m.to),
      );
      v.dispatch({
        selection: EditorSelection.create(ranges, 0),
        userEvent: "select.search",
      });
      return { cursors: take.length, capped };
    },

    dispose() {
      queryText = "";
      scope = null;
      activeIndex = -1;
      lastCount = { total: 0, activeIndex: -1, capped: false };
      const v = view();
      if (v) {
        v.dispatch({
          effects: setSearchQuery.of(
            new SearchQuery({ search: "", literal: true }),
          ),
        });
      }
    },
  };
}
