import { Show, createEffect, createSignal, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import type { SourceWalkTurnSummary } from "../../api/types.ts";
import { prepareWalk } from "../../files/walk/walk-prefetch.ts";
import { enterWalk } from "../../files/walk/walk-store.ts";
import {
  cachedWalkPreview,
  subscribeWalkPreview,
} from "../../files/walk/walk-preview.ts";

type Props = {
  projectId: string;
  sessionId: string;
  messageId: string;
  client: LycaonClient | null | undefined;
};

export function TurnWalkCard(props: Props) {
  // Cached content preserves row height on the first render.
  const [summary, setSummary] = createSignal<SourceWalkTurnSummary | null>(
    untrack(() =>
      cachedWalkPreview(props.projectId.trim(), props.sessionId.trim(), props.messageId) ?? null,
    ),
  );
  createEffect(() => {
    const client = props.client;
    const projectId = props.projectId.trim();
    const sessionId = props.sessionId.trim();
    if (!client || !projectId || !sessionId) {
      setSummary(null);
      return;
    }
    onCleanup(subscribeWalkPreview(client, projectId, sessionId, props.messageId, setSummary));
  });

  const prepare = () => {
    if (props.client && summary()) prepareWalk(props.client, props.projectId, props.sessionId, undefined, props.messageId);
  };

  const startWalk = () => {
    const client = props.client;
    const value = summary();
    if (!client || !value) return;
    void enterWalk(props.projectId, client, props.sessionId, undefined, {
      transition: "replace", startMessageId: props.messageId,
    });
    openFilesSurface({ kind: "stage", projectId: props.projectId });
  };

  const openDiffs = () => {
    const value = summary();
    if (!value || value.turn < 1) return;
    openFilesSurface({
      kind: "diffs",
      projectId: props.projectId,
      address: { kind: "turn", sessionId: props.sessionId, turn: value.turn, messageId: props.messageId },
    });
    openFilesSurface({ kind: "stage", projectId: props.projectId });
  };

  return (
    <Show when={summary()?.steps ? summary() : null}>
      {(value) => (
        <aside class="den-turn-walk" data-testid="turn-walk-card">
          <div class="den-turn-walk__copy">
            <strong>Changes in this turn</strong>
            <span>
              {value().steps} {value().steps === 1 ? "step" : "steps"}
              <Show when={value().items > 0}>
                {" "}across {value().items} {value().items === 1 ? "item" : "items"}
              </Show>
            </span>
          </div>
          <div class="den-turn-walk__actions">
            <Show when={value().items > 0 && value().turn > 0}>
              <button type="button" class="den-turn-walk__action den-turn-walk__action--quiet"
                data-testid="turn-diffs-action" onClick={openDiffs}>
                <ThemeIcon slot="diff" size={15} />
                All diffs
              </button>
            </Show>
            <button type="button" class="den-turn-walk__action"
              data-testid="turn-walk-action" onPointerEnter={prepare} onFocus={prepare} onClick={startWalk}>
              <ThemeIcon slot="walk" size={15} />
              Walk from here
            </button>
          </div>
        </aside>
      )}
    </Show>
  );
}
