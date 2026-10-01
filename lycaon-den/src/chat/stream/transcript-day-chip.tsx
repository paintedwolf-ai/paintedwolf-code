import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import { useNow } from "../../time/now.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { streamTranscriptEl, subscribeStreamScroll } from "./stream-scroll.ts";
import { useTranscriptViewport } from "./transcript-viewport.tsx";

/** Keeps the current day visible after its transcript label scrolls away. */
export function TranscriptDayChip() {
  const viewport = useTranscriptViewport();
  const [day, setDay] = createSignal<number | null>(null);
  const now = useNow("minute", () => day() !== null);
  const calendar = viewerCalendarTimeFormat();

  createEffect(() => {
    const el = viewport?.stream();
    if (!viewport || !el) {
      setDay(null);
      return;
    }
    let frame: number | undefined;
    const sync = () => {
      frame = undefined;
      setDay(viewport.readingDay());
    };
    const schedule = () => {
      if (frame === undefined) frame = requestAnimationFrame(sync);
    };
    const unsubscribe = subscribeStreamScroll(el, schedule);
    // Older history arriving above the reader can change the day under them.
    const ro = new ResizeObserver(schedule);
    ro.observe(streamTranscriptEl(el));
    sync();
    onCleanup(() => {
      unsubscribe();
      ro.disconnect();
      if (frame !== undefined) cancelAnimationFrame(frame);
    });
  });

  // Keep the last label while the chip fades out.
  const [shown, setShown] = createSignal<number | null>(null);
  createEffect(() => {
    const next = day();
    if (next !== null) setShown(next);
  });

  return (
    <div
      class="den-transcript-day-chip"
      classList={{ "den-transcript-day-chip--visible": day() !== null }}
      data-testid="transcript-day-chip"
      {...chromeProps()}
    >
      <Show when={shown()} keyed>
        {(at) => <span data-tip={calendar.fullDate(at)}>{calendar.dayLabel(at, now())}</span>}
      </Show>
    </div>
  );
}
