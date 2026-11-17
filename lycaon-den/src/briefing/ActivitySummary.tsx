import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import { BriefingMark } from "./BriefingSummary.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { splitBriefingInline } from "./briefing-inline.ts";
import { SUMMARY_FADE_CLASS, createSummaryFade } from "./summary-fade.ts";

/** What an opened activity's summary region shows. */
export type ActivitySummaryState =
  | { kind: "pending"; purpose?: string }
  | { kind: "ready"; purpose?: string; change?: string; recordId: string }
  | { kind: "missing"; purpose?: string };

type Props = {
  state: ActivitySummaryState;
  /** The activity has not settled, so no change is expected yet. */
  running: boolean;
  onRetry: () => void;
};

function Inline(props: { text: string }) {
  return (
    <For each={splitBriefingInline(props.text)}>
      {(part) => (
        <Show when={part.kind === "code"} fallback={part.text}>
          <code class="den-briefing-summary__code">{part.text}</code>
        </Show>
      )}
    </For>
  );
}

/** Each section retains its reserved space until the person expands it. */
function ActivitySummarySection(props: {
  id: "intent" | "outcome";
  label: string;
  text?: string;
  waiting: string;
  missing: boolean;
  onRetry: () => void;
}) {
  const held = !props.text;
  let section: HTMLDivElement | undefined;
  let measure: HTMLParagraphElement | undefined;
  const [clamped, setClamped] = createSignal(false);
  const [expanded, setExpanded] = createSignal(false);
  // Measure an unclamped copy because the visible text reports only its clipped height.
  createEffect(() => {
    void props.text;
    const sectionEl = section;
    const measureEl = measure;
    if (!sectionEl || !measureEl || expanded() || typeof ResizeObserver === "undefined") return;
    const check = () => {
      const style = getComputedStyle(sectionEl);
      const lines = Number.parseFloat(style.getPropertyValue("--den-activity-summary-lines")) || 3;
      const lineHeight = Number.parseFloat(getComputedStyle(measureEl).lineHeight) || 0;
      setClamped(lineHeight > 0 && measureEl.getBoundingClientRect().height > lines * lineHeight + 1);
    };
    const observer = new ResizeObserver(check);
    observer.observe(measureEl);
    check();
    onCleanup(() => observer.disconnect());
  });
  return (
    <div
      ref={(el) => (section = el)}
      class="den-activity-summary__section"
      data-section={props.id}
      data-held={held && !expanded() ? "" : undefined}
      data-clamped={clamped() && !expanded() ? "" : undefined}
    >
      <BriefingMark />
      <div class="den-activity-summary__body">
        <p class="den-activity-summary__text" data-expanded={expanded() ? "" : undefined}>
          <strong class="den-briefing-summary__label">{props.label}:</strong>{" "}
          <Show when={props.text} keyed fallback={
            <span class="den-activity-summary__waiting">
              {props.missing ? briefingCopy.notArrived : props.waiting}
            </span>
          }>
            {(text) => <Inline text={text} />}
          </Show>
        </p>
        <p class="den-activity-summary__measure" aria-hidden="true" ref={(el) => (measure = el)}>
          <strong class="den-briefing-summary__label">{props.label}:</strong>{" "}<Inline text={props.text ?? ""} />
        </p>
        <Show when={props.missing && !props.text}>
          <button type="button" class="den-chat-content-link" data-testid="activity-summary-retry" aria-label={`Retry ${props.id} summary`} onClick={() => props.onRetry()}>{briefingCopy.retry}</button>
        </Show>
        <Show when={clamped() && !expanded()}>
          <button type="button" class="den-chat-content-link den-activity-summary__more" data-testid="activity-summary-show-all" aria-label={`Show all ${props.id}`} onClick={() => setExpanded(true)}>{briefingCopy.showAll}</button>
        </Show>
      </div>
    </div>
  );
}

export function ActivitySummary(props: Props) {
  const ready = () => props.state.kind === "ready" ? props.state : undefined;
  const fade = createSummaryFade(() => ready() ? `${ready()!.recordId}\0activity` : undefined);
  return (
    <div class="den-activity-summary-frame">
      <div class="den-activity-summary" data-testid="activity-summary" data-state={props.state.kind}>
        <div class="den-activity-summary__sections" classList={{ [SUMMARY_FADE_CLASS]: fade() }}>
        <ActivitySummarySection
          id="intent" label="Intent" text={props.state.purpose}
          waiting={briefingCopy.waiting}
          missing={props.state.kind === "missing" || (props.state.kind === "ready" && !props.state.purpose)}
          onRetry={props.onRetry}
        />
        <ActivitySummarySection
          id="outcome" label="Outcome" text={ready()?.change}
          waiting={props.running ? briefingCopy.resultPending : briefingCopy.waiting}
          missing={!props.running && (props.state.kind === "missing" || (props.state.kind === "ready" && !props.state.change))}
          onRetry={props.onRetry}
        />
        </div>
      </div>
    </div>
  );
}
