import { Show } from "solid-js";
import type { TranscriptTimeItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import { useNow } from "../../time/now.ts";
import { formatDuration } from "../../time/time-copy.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";

/** Work under a second is not shown. */
const MIN_WORK_SHOWN_MS = 1000;

const isoOf = (at: number) => new Date(at).toISOString();

/** A day label between rows, or the clock time before a prompt after a quiet hour. */
export function TranscriptTimeMarker(props: {
  item: Extract<TranscriptTimeItem, { kind: "time_marker" }>;
}) {
  const now = useNow("minute");
  const calendar = viewerCalendarTimeFormat();
  return (
    <Show
      when={props.item.variant === "day"}
      fallback={
        <p class="den-transcript-gap" data-testid="transcript-gap-time" {...chromeProps()}>
          <time datetime={isoOf(props.item.at)} data-tip={calendar.fullDateTime(props.item.at)}>
            {calendar.stamp(props.item.at, now())}
          </time>
        </p>
      }
    >
      <div class="den-transcript-day" data-testid="transcript-day" {...chromeProps()}>
        <time
          class="den-transcript-day__label"
          datetime={isoOf(props.item.at)}
          data-tip={calendar.fullDate(props.item.at)}
        >
          {calendar.dayLabel(props.item.at, now())}
          <span class="den-visually-hidden">, {calendar.fullDate(props.item.at)}</span>
        </time>
      </div>
    </Show>
  );
}

/** Where rows begin that arrived after the person's previous look at this chat. */
export function TranscriptUnreadMarker(props: {
  item: Extract<TranscriptTimeItem, { kind: "unread_marker" }>;
}) {
  const now = useNow("minute");
  const calendar = viewerCalendarTimeFormat();
  const lastLooked = () =>
    calendar.dayKey(props.item.seenAt) === calendar.dayKey(now())
      ? `at ${calendar.time(props.item.seenAt)}`
      : `on ${calendar.fullDateTime(props.item.seenAt)}`;
  return (
    <div class="den-transcript-unread" data-testid="transcript-unread" {...chromeProps()}>
      <span class="den-transcript-unread__rule" aria-hidden="true" />
      <span class="den-transcript-unread__label" data-tip={`You last looked ${lastLooked()}`}>
        New
        <span class="den-visually-hidden">, you last looked {lastLooked()}</span>
      </span>
    </div>
  );
}

/** The quiet anchor in a turn seam: when the turn settled, and how long it worked. */
export function TurnTail(props: {
  item: Extract<TranscriptTimeItem, { kind: "turn_tail" }>;
}) {
  const now = useNow("minute");
  const calendar = viewerCalendarTimeFormat();
  const workTip = () => {
    const started = props.item.startedAt === null ? "" : `Started ${calendar.time(props.item.startedAt)}. `;
    return `${started}Time spent waiting on you isn't counted.`;
  };
  return (
    <p
      class="den-turn-tail"
      data-testid="turn-tail"
      aria-hidden={props.item.settledAt === null ? true : undefined}
      {...chromeProps()}
    >
      <Show when={props.item.settledAt === null ? undefined : { at: props.item.settledAt }}>
        {(settled) => <>
          <time
            datetime={isoOf(settled().at)}
            data-tip={calendar.fullDateTime(settled().at)}
          >
            {calendar.finished(settled().at, now())}
          </time>
          <Show when={props.item.workMs >= MIN_WORK_SHOWN_MS}>
            <span class="den-turn-tail__dot" aria-hidden="true">·</span>
            <span data-tip={workTip()} data-testid="turn-tail-work">
              Worked {formatDuration(props.item.workMs)}
            </span>
          </Show>
        </>}
      </Show>
    </p>
  );
}

/** When one of the person's own messages was sent: in its hover toolbar, or below it. */
export function MessageTime(props: { ts: string; placement: "toolbar" | "below" }) {
  const now = useNow("minute");
  const calendar = viewerCalendarTimeFormat();
  const at = () => Date.parse(props.ts);
  return (
    <Show when={Number.isFinite(at())}>
      <time
        class={props.placement === "toolbar" ? "den-msg-actions__time" : "den-msg-time-below"}
        datetime={props.ts}
        data-tip={calendar.fullDateTime(at())}
        data-tip-pos={props.placement === "toolbar" ? "below" : undefined}
        data-testid="message-time"
        {...chromeProps()}
      >
        {calendar.stamp(at(), now())}
      </time>
    </Show>
  );
}
