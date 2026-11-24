import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { StandingWatch, WatchRun } from "../../api/types.ts";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { formatWatchRunStatus } from "../../watches/watch-format.ts";
import { DenButton } from "../primitives/DenButton.tsx";

type Props = {
  projectId: string;
  watch: StandingWatch;
  client: () => LycaonClient | null;
  onOpenSession: (sessionId: string) => void;
};

function formatTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? iso : formatRelativeTime(ms);
}

/** Newest-first run history list inside a selected standing watch's detail pane. */
export function StandingWatchRunHistory(props: Props) {
  const [extraRuns, setExtraRuns] = createSignal<readonly WatchRun[]>([]);
  const [nextCursor, setNextCursor] = createSignal<string | null>(null);
  const [loadingMore, setLoadingMore] = createSignal(false);

  createEffect(() => {
    const watchId = props.watch.watch_id;
    const pid = props.projectId;
    const client = props.client();
    setExtraRuns([]);
    setNextCursor(null);
    if (!client || !pid || !watchId) return;
    let cancelled = false;
    void client
      .listWatchRuns(pid, watchId, { limit: 20 })
      .then((res) => {
        if (cancelled) return;
        setExtraRuns(res.runs);
        setNextCursor(res.next_cursor ?? null);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  });

  const mergedRuns = createMemo(() => {
    const byId = new Map<string, WatchRun>();
    if (props.watch.active_run) {
      byId.set(props.watch.active_run.run_id, props.watch.active_run);
    }
    for (const r of props.watch.recent_runs ?? []) {
      if (!byId.has(r.run_id)) byId.set(r.run_id, r);
    }
    for (const r of extraRuns()) {
      if (!byId.has(r.run_id)) byId.set(r.run_id, r);
    }
    return [...byId.values()].sort((a, b) =>
      b.created_at.localeCompare(a.created_at),
    );
  });

  const loadOlder = async () => {
    const cursor = nextCursor();
    const client = props.client();
    if (!cursor || !client || loadingMore()) return;
    setLoadingMore(true);
    try {
      const res = await client.listWatchRuns(
        props.projectId,
        props.watch.watch_id,
        { cursor, limit: 20 },
      );
      setExtraRuns((prev) => [...prev, ...res.runs]);
      setNextCursor(res.next_cursor ?? null);
    } finally {
      setLoadingMore(false);
    }
  };

  return (
    <section
      class="flex flex-col gap-den-2 border-t border-den-line pt-den-3"
      aria-label="Run history"
      data-testid="standing-watch-history"
    >
      <h4 class="text-den-body font-medium text-den-text-primary">
        Run history
      </h4>
      <Show
        when={mergedRuns().length > 0}
        fallback={
          <p
            class="text-den-caption text-den-text-muted"
            data-testid="standing-watch-history-empty"
          >
            No runs recorded yet.
          </p>
        }
      >
        <ul class="flex flex-col gap-den-1-5" data-testid="standing-watch-history-list">
          <For each={mergedRuns()}>
            {(run) => {
              const stamp = run.ended_at ?? run.started_at ?? run.scheduled_at;
              const stampMs = Date.parse(stamp);
              return (
                <li
                  class="flex flex-wrap items-center justify-between gap-den-2 rounded-den-sm border border-den-line p-den-2 text-den-caption"
                  data-testid={`standing-watch-history-row-${run.run_id}`}
                >
                  <div class="flex flex-wrap items-center gap-den-2">
                    <span
                      class="font-medium text-den-text-primary"
                      data-testid={`standing-watch-history-status-${run.run_id}`}
                    >
                      {formatWatchRunStatus(run.status)}
                    </span>
                    <span
                      class="text-den-text-muted"
                      data-tip={
                        Number.isNaN(stampMs)
                          ? undefined
                          : viewerCalendarTimeFormat().fullDateTime(stampMs)
                      }
                    >
                      {formatTime(stamp)}
                    </span>
                    <Show when={run.missed_count > 0}>
                      <span
                        class="text-den-text-muted"
                        data-testid={`standing-watch-history-missed-${run.run_id}`}
                      >
                        Missed {run.missed_count}
                      </span>
                    </Show>
                  </div>
                  <Show when={run.session_id} keyed>
                    {(sid) => (
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid={`standing-watch-history-chat-${run.run_id}`}
                        onClick={() => props.onOpenSession(sid)}
                      >
                        Open chat
                      </DenButton>
                    )}
                  </Show>
                </li>
              );
            }}
          </For>
        </ul>
      </Show>
      <Show when={nextCursor()}>
        <div>
          <DenButton
            variant="ghost"
            compact
            disabled={loadingMore()}
            data-testid="standing-watch-history-more"
            onClick={() => void loadOlder()}
          >
            {loadingMore() ? "Loading…" : "Load older runs"}
          </DenButton>
        </div>
      </Show>
    </section>
  );
}
