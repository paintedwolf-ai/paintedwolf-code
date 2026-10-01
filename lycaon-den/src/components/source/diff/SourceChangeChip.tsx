import { Match, Switch } from "solid-js";
import type { SourceReaderChange } from "../reader/source-reader-change.ts";

/** Names a whole-file change in words, beside a file name that carries it in color. */
export function SourceChangeChip(props: { change: SourceReaderChange }) {
  return (
    <Switch>
      <Match when={props.change === "added"}>
        <span class="den-status-mark" data-tone="positive" data-testid="source-change-chip">New file</span>
      </Match>
      <Match when={props.change === "deleted"}>
        <span class="den-status-mark" data-tone="danger" data-testid="source-change-chip">Deleted file</span>
      </Match>
    </Switch>
  );
}
