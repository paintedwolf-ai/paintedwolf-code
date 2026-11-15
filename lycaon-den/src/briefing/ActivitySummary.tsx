import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import { BriefingMark } from "./BriefingSummary.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { splitBriefingInline } from "./briefing-inline.ts";
import { SUMMARY_FADE_CLASS, claimSummaryFade, markSummaryShown } from "./summary-fade.ts";

/** What an opened activity's summary region shows. */
export type ActivitySummaryState =
  | { kind: "pending"; purpose?: string }
  | { kind: "ready"; purpose?: string; change?: string; recordId: string; changePending: boolean }
  | { kind: "missing"; purpose?: string };

type Props = {
  state: ActivitySummaryState;
  /** The activity has not settled, so no change is expected yet. */
  running: boolean;
  onRetry: () => void;
};

function Inline(props: { text: string }) {
  return (
    <>
      {splitBriefingInline(props.text).map((part) => part.kind === "code"
        ? <code class="den-briefing-summary__code">{part.text}</code>
        : part.text)}
    </>
  );
}

/**
 * One boxed paragraph: why the activity ran, then what it changed. When the
 * card opens before a summary is stored, the box holds its line count for
 * the whole opening so the arrival moves nothing below it.
 */
export function ActivitySummary(props: Props) {
  const held = props.state.kind !== "ready";
  const fadeKey = (recordId: string) => `${recordId}\0activity`;
  if (props.state.kind === "ready") markSummaryShown(fadeKey(props.state.recordId));
  let text: HTMLParagraphElement | undefined;
  const [clamped, setClamped] = createSignal(false);
  const [expanded, setExpanded] = createSignal(false);
  createEffect(() => {
    void props.state;
    if (!text || expanded() || typeof ResizeObserver === "undefined") return;
    const measure = () => setClamped(text!.scrollHeight > text!.clientHeight + 1);
    const observer = new ResizeObserver(measure);
    observer.observe(text);
    measure();
    onCleanup(() => observer.disconnect());
  });
  const later = () => props.running ? briefingCopy.resultPending : briefingCopy.waiting;
  return (
    <div class="den-activity-summary-frame">
      <div
        class="den-activity-summary"
        data-testid="activity-summary"
        data-state={props.state.kind}
        data-held={held && !expanded() ? "" : undefined}
        data-clamped={clamped() && !expanded() ? "" : undefined}
      >
        <BriefingMark />
        <div class="den-activity-summary__body">
          <Show when={props.state.kind === "ready" ? props.state : undefined} keyed fallback={
            <p class="den-activity-summary__text" ref={(el) => (text = el)}>
              <Show when={props.state.purpose}>{(purpose) => <><span class="den-activity-summary__purpose"><Inline text={purpose()} /></span>{" "}</>}</Show>
              <Show when={props.state.kind === "missing"} fallback={<span class="den-activity-summary__waiting">{briefingCopy.waiting}</span>}>
                <span class="den-activity-summary__waiting">{briefingCopy.notArrived}</span>{" "}
                <button type="button" class="den-chat-content-link" data-testid="activity-summary-retry" onClick={() => props.onRetry()}>{briefingCopy.retry}</button>
              </Show>
            </p>
          }>
            {(ready) => (
              <p
                class="den-activity-summary__text"
                classList={{ [SUMMARY_FADE_CLASS]: claimSummaryFade(fadeKey(ready.recordId)) }}
                data-expanded={expanded() ? "" : undefined}
                ref={(el) => (text = el)}
              >
                <Show when={ready.purpose}>{(purpose) => <><span class="den-activity-summary__purpose"><Inline text={purpose()} /></span>{" "}</>}</Show>
                <Show when={ready.change} fallback={<Show when={ready.changePending || props.running}><span class="den-activity-summary__waiting">{later()}</span></Show>}>
                  {(change) => <span class="den-activity-summary__change"><Inline text={change()} /></span>}
                </Show>
              </p>
            )}
          </Show>
          <Show when={clamped() && !expanded()}>
            <button type="button" class="den-chat-content-link den-activity-summary__more" data-testid="activity-summary-show-all" onClick={() => setExpanded(true)}>{briefingCopy.showAll}</button>
          </Show>
        </div>
      </div>
    </div>
  );
}
