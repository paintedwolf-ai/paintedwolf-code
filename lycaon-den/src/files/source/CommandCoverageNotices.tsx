import { For, Show } from "solid-js";
import type { CommandCoverageNotice } from "./source-command-window.ts";

type Props = {
  notices: readonly CommandCoverageNotice[];
  class: string;
  testId: string;
};

export function CommandCoverageNotices(props: Props) {
  return (
    <For each={props.notices}>
      {(notice) => (
        <div class={props.class} data-testid={props.testId} data-folder={notice.folder}>
          <p>{notice.text}</p>
          <Show when={notice.skipped.length > 0}>
            <ul>
              <For each={notice.skipped}>
                {(skipped) => (
                  <li>
                    <span class="break-all">{skipped.path}</span> · {skipped.reason}
                  </li>
                )}
              </For>
              <Show when={notice.more > 0}>
                <li>{notice.more} more</li>
              </Show>
            </ul>
          </Show>
        </div>
      )}
    </For>
  );
}
