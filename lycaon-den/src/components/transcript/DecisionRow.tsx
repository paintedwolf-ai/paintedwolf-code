import { For, Show } from "solid-js";
import { cn } from "../../shared/cn.ts";

export type DecisionTone = "approved" | "rejected" | "pending" | "neutral";

export type DecisionDetail = {
  text: string;
  /** Mono + slightly heavier: a tool, a path, a subject the reader scans for. */
  emphasis?: "subject" | "grant" | "plain";
  /** Clipping preserves a single-line summary. */
  truncate?: boolean;
  testId?: string;
};

type Props = {
  tone: DecisionTone;
  label: string;
  details?: readonly DecisionDetail[];
  class?: string;
};

/** Shared one-line decision layout. */
export function DecisionRow(props: Props) {
  return (
    <span class={cn("den-decision", props.class)} data-tone={props.tone}>
      <span class="den-decision__mark" aria-hidden="true" />
      <span class="den-decision__label">{props.label}</span>
      <For each={[...(props.details ?? [])]}>
        {(detail) => (
          <Show when={detail.text.trim()}>
            <span class="den-decision__sep" aria-hidden="true">
              ·
            </span>
            <span
              class="den-decision__detail"
              data-emphasis={detail.emphasis ?? "plain"}
              classList={{ "den-decision__detail--truncate": detail.truncate }}
              data-testid={detail.testId}
              data-tip={detail.truncate ? detail.text : undefined}
              data-tip-when-clipped
            >
              {detail.text}
            </span>
          </Show>
        )}
      </For>
    </span>
  );
}

/** One line for a screen reader: the same row, read as a sentence. */
export function decisionRowLabel(
  label: string,
  details: readonly (string | undefined)[],
): string {
  return [label, ...details.map((d) => d?.trim()).filter(Boolean)].join(" · ");
}
