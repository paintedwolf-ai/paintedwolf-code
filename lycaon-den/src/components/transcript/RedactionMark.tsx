import { For, Show } from "solid-js";

import {
  REDACTION_MARK_CLASS,
  redactionMarkModifier,
  redactionMarkTitle,
  segmentBySpans,
  spansForField,
  type RedactionSegment,
} from "../../chat/transcript/content/redaction-spans.ts";
import type { Message, RedactedSpan } from "../../api/types.ts";

/** Paints the durable marker used by copy and assistive reading. */
export function RedactionMark(props: { span: RedactedSpan; text: string }) {
  return (
    <span
      class={[REDACTION_MARK_CLASS, redactionMarkModifier(props.span)].filter(Boolean).join(" ")}
      data-tip={redactionMarkTitle(props.span)}
      data-redaction-kind={props.span.kind}
      data-redaction-rule={props.span.rule_id ?? undefined}
    >
      {props.text}
    </span>
  );
}

type MarkSegment = Extract<RedactionSegment, { kind: "mark" }>;

function asMark(segment: RedactionSegment): MarkSegment | undefined {
  return segment.kind === "mark" ? segment : undefined;
}

/** Renders one redacted field, marking every span the host recorded. */
export function RedactedText(props: {
  message: Pick<Message, "host_secret_redaction"> | undefined;
  field: string;
  text: string;
}) {
  const segments = (): RedactionSegment[] =>
    segmentBySpans(props.text, spansForField(props.message, props.field));
  const marked = () => segments().some((segment) => segment.kind === "mark");
  return (
    <Show when={marked()} fallback={<>{props.text}</>}>
      <For each={segments()}>
        {(segment) => (
          <Show when={asMark(segment)} keyed fallback={<>{segment.text}</>}>
            {(mark) => <RedactionMark span={mark.span} text={mark.text} />}
          </Show>
        )}
      </For>
    </Show>
  );
}
