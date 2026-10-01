import { Show, createMemo, createSignal, onCleanup } from "solid-js";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { walkChapterAt } from "./walk-chapters.ts";
import { subscribeWalk, walkState } from "./walk-store.ts";

export function WalkChapterHeading(props: {
  projectId: string;
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
}) {
  const [tick, setTick] = createSignal(0);
  onCleanup(subscribeWalk((id) => {
    if (id === props.projectId.trim()) setTick((value) => value + 1);
  }));
  const state = createMemo(() => {
    void tick();
    return walkState(props.projectId);
  });
  const chapter = createMemo(() => walkChapterAt(state().walk, state().at));

  return (
    <Show when={chapter()}>
      {(current) => (
        <div class="den-walk-chapter">
          <div class="den-walk-chapter__label" data-testid="walk-chapter-heading">
            <b>{current().title}</b>
            <span class="den-walk-chapter__excerpt">
              {current().prompt || (current().turn ? "Recorded changes" : "Changes outside a chat turn")}
            </span>
          </div>
          <Show when={current().messageId && props.onRevealInTranscript}>
            <button type="button" class="den-walk-chapter__reveal" onClick={() => props.onRevealInTranscript?.({
              sessionId: current().sessionId,
              anchor: { chicklet: "message", anchorId: current().messageId },
            })}>Show in chat</button>
          </Show>
        </div>
      )}
    </Show>
  );
}
