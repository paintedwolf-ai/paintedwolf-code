import { For, Show } from "solid-js";
import { homeLaunches } from "../../home/launch/home-launches.ts";
import { launchSummary } from "../../home/launch/launch-status.ts";
import type { LaunchRecord } from "../../home/launch/home-launch-model.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";

function ideaLabel(record: LaunchRecord): string {
  const line = record.text.split("\n").find((part) => part.trim())?.trim();
  return line || record.chips[0]?.name || "New idea";
}

/** Submitted ideas that have not reached their chat yet; each reopens its conversation. */
export function PendingLaunches(props: { onOpen: (launchId: string) => void }) {
  const launches = () => homeLaunches.pending();
  return (
    <Show when={launches().length > 0}>
      <section class="attention-band" data-testid="pending-launches" aria-label="Ideas on their way to a chat">
        <div class="attention-band__head" {...chromeProps()}>
          <span class="attention-band__title">
            {launches().length === 1 ? "1 idea is starting" : `${launches().length} ideas are starting`}
          </span>
        </div>
        <ul class="attention-band__list">
          <For each={launches()}>
            {(record) => (
              <li>
                <button
                  type="button"
                  class="attention-band__row"
                  data-testid="pending-launch-row"
                  data-launch-id={record.launchId}
                  aria-label={`${ideaLabel(record)} — ${launchSummary(homeLaunches.runtime(record.launchId))}`}
                  onClick={() => props.onOpen(record.launchId)}
                >
                  <span class="attention-band__primary">{ideaLabel(record)}</span>
                  <span class="attention-band__reason">{launchSummary(homeLaunches.runtime(record.launchId))}</span>
                </button>
              </li>
            )}
          </For>
        </ul>
      </section>
    </Show>
  );
}
