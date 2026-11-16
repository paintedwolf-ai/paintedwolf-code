import { For, Index, Match, Show, Switch, createMemo, createResource } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ActionPreview as ActionPreviewData } from "../../api/types.ts";
import { actionOutput } from "../../chat/action/action-output.ts";
import type { CardAction } from "../../chat/action/action-merge.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";

/** Executions read per activity for an output preview; the Receipt link has them all. */
const PREVIEW_EXECUTIONS = 6;
/** Activities read for one preview. */
const PREVIEW_SCOPES = 3;

type Props = {
  preview: ActionPreviewData;
  action: CardAction;
  part: ToolPartView;
  sessionId?: string;
  client?: LycaonClient | null;
};

/** Output, diff, hits or step lines, at most four, on an inset. */
export function ActionPreview(props: Props) {
  return (
    <Switch>
      <Match when={props.preview.kind === "output" && props.preview.receipts?.length}>
        <ReceiptOutput {...props} />
      </Match>
      <Match when={props.preview.lines?.length}>
        <div class="den-action-card__preview" data-testid="action-preview" data-kind={props.preview.kind}>
          <Index each={props.preview.lines}>
            {(line) => (
              <div class="den-action-card__preview-line" data-sign={line().sign} data-stream={line().stream} data-state={line().state}>
                <Show when={line().location}>{(location) => <span class="den-action-card__preview-location">{location()}</span>}</Show>
                <span>{line().sign === "added" ? "+ " : line().sign === "removed" ? "− " : ""}{line().text}</span>
              </div>
            )}
          </Index>
          <Show when={props.preview.more}>{(more) => <p class="den-action-card__preview-note">{more().toLocaleString()} more</p>}</Show>
        </div>
      </Match>
    </Switch>
  );
}

/** Output kept in execution receipts, read when the card opens. */
function ReceiptOutput(props: Props) {
  const request = createMemo(() => {
    const client = props.client ?? getLycaonClient();
    if (!client || !props.sessionId) return undefined;
    const scopes = (props.preview.receipts ?? []).slice(0, PREVIEW_SCOPES);
    return { client, sessionId: props.sessionId, scopes, key: JSON.stringify([scopes, props.action.status]) };
  }, undefined, { equals: (a, b) => a?.client === b?.client && a?.sessionId === b?.sessionId && a?.key === b?.key });
  const [pages] = createResource(request, ({ client, sessionId, scopes }) =>
    Promise.all(scopes.map((scope) => client.getCommandProgramReceipt(sessionId, scope.program_id, { limit: PREVIEW_EXECUTIONS, cursor: undefined, activity_id: scope.activity_id || undefined }))));
  const output = createMemo(() => {
    const loaded = pages.state === "ready" || pages.state === "refreshing" ? pages.latest ?? [] : [];
    return actionOutput(props.action, loaded.flatMap((page) => page?.executions ?? []), props.part.output);
  });
  return (
    <Show when={output()}>
      {(shown) => (
        <div class="den-action-card__preview" data-testid="action-preview" data-kind="output" data-stream={shown().stream} data-tail={shown().tail ? "" : undefined}>
          <For each={shown().lines}>{(line) => <div class="den-action-card__preview-line">{line}</div>}</For>
          <Show when={shown().hidden}>{(hidden) => <p class="den-action-card__preview-note">{hidden()}</p>}</Show>
        </div>
      )}
    </Show>
  );
}
