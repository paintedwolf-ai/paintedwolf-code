import { Show } from "solid-js";
import type { ProgressState, ProgressStep } from "../api/types.ts";
import { MarkdownBody } from "./transcript/MarkdownBody.tsx";

const INLINE_MARKDOWN_SYNTAX_RE = /[*_`~[\]\\<>#&]|^\s*(?:[-+>]|\d+\.)\s/;

export function progressStateGlyph(state: ProgressState): string {
  switch (state) {
    case "done":
      return "✓";
    case "na":
      return "–";
    default:
      return "○";
  }
}

/** A step label; inline formatting renders only when the label carries any. */
export function ProgressStepLabel(props: { label: string }) {
  return (
    <Show
      when={INLINE_MARKDOWN_SYNTAX_RE.test(props.label)}
      fallback={<div class="markdown-body">{props.label}</div>}
    >
      <MarkdownBody inline literalHtml source={props.label} />
    </Show>
  );
}

/** One checklist row: the step's state glyph and its label. */
export function ProgressStepRow(props: { step: ProgressStep; leaving?: boolean }) {
  return (
    <li
      class="progress-strip__item"
      classList={{
        "progress-strip__item--done": props.step.state === "done",
        "progress-strip__item--na": props.step.state === "na",
        "progress-strip__item--leaving": props.leaving === true,
      }}
      data-testid="progress-strip-item"
      data-kind="plan"
      data-state={props.step.state}
    >
      <span class="progress-strip__glyph" aria-hidden="true">
        {progressStateGlyph(props.step.state)}
      </span>
      <div class="progress-strip__label">
        <ProgressStepLabel label={props.step.label} />
      </div>
    </li>
  );
}
