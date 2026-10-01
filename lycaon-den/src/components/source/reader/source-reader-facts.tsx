import { createSignal, onCleanup } from "solid-js";
import type { SourceContributor } from "../../../api/types.ts";
import { LineFactsCard, type LineFactsCardState } from "../annotations/LineFactsCard.tsx";
import type { ReaderEditor } from "./source-reader-editor.ts";
import { sourceContributorLabel } from "../annotations/source-contributor-label.ts";

export function createReaderFacts(path: () => string, activate: (author: SourceContributor, line: number) => void) {
  const [state, setState] = createSignal<LineFactsCardState | null>(null);
  let authors: SourceContributor[] = [];
  let timer: ReturnType<typeof setTimeout> | undefined;
  const keep = () => { clearTimeout(timer); timer = undefined; };
  const hide = () => {
    keep();
    const held = state();
    held?.anchor.classList.remove("is-open");
    held?.anchor.querySelector(".files-line-gutter__facts")?.setAttribute("aria-expanded", "false");
    setState(null);
  };
  const leave = () => { keep(); timer = setTimeout(hide, 160); };
  onCleanup(hide);
  return {
    leave,
    show(editor: ReaderEditor, line: number, anchor: HTMLElement, enter: boolean) {
      keep();
      const entry = editor.document.at(editor.view.state.doc.line(line).from);
      const row = entry?.row;
      authors = row?.contributors?.filter(author => author.session_id) ?? [];
      if (!row || !authors.length) { hide(); return; }
      const sourceLine = row.after_line || row.before_line;
      const changed = authors.map((author, index) => ({ key: String(index), title: sourceContributorLabel(author), fact: `Changed line ${sourceLine}`,
        target: { kind: "chat" as const, sessionId: author.session_id ?? "", toolCallId: author.tool_call_id ?? "" },
      }));
      const held = state();
      if (held?.anchor !== anchor) {
        held?.anchor.classList.remove("is-open");
        held?.anchor.querySelector(".files-line-gutter__facts")?.setAttribute("aria-expanded", "false");
      }
      anchor.classList.add("is-open");
      anchor.querySelector(".files-line-gutter__facts")?.setAttribute("aria-expanded", "true");
      setState({ anchor, line: sourceLine, path: path(), enter, facts: { now: [], changed, findings: [], count: changed.length } });
    },
    card: <LineFactsCard state={state()} onActivate={(row, line) => { const author = authors[Number(row.key)]; if (author) activate(author, line); hide(); }}
      onKeepOpen={keep} onLeave={leave} onDismiss={hide} onReturnFocus={() => {
        const anchor = state()?.anchor.querySelector<HTMLButtonElement>(".files-line-gutter__facts"); hide(); anchor?.focus();
      }} />,
  };
}
