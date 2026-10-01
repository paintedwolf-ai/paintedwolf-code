import { Show } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { VisualArtifact } from "../../api/types.ts";
import { useArtifactDedup } from "../../chat/visual/artifact-dedup-context.tsx";
import { KeyedIndex } from "../keyed-index.tsx";
import { visualPresentEntryKey } from "../../chat/visual/visual-artifact-canonical.ts";
import { TranscriptVisualSlot } from "./TranscriptVisualSlot.tsx";

type Props = {
  artifactIds: readonly string[];
  sessionId?: string | null;
  projectId?: string;
  client?: LycaonClient | null;
  rowKey: string;
};

/** Present strip. First site per id on this face is full. */
export function VisualPresentBlock(props: Props) {
  const dedup = useArtifactDedup();
  const artifacts = (): VisualArtifact[] =>
    props.artifactIds
      .map((id) => id.trim())
      .filter(Boolean)
      .map((id) => {
        const meta = dedup?.metaFor(id);
        const site = dedup?.siteFor(id, "present");
        return {
          id,
          mime: meta?.mime.trim() || site?.mime.trim() || "image/png",
          source: meta?.source ?? site?.source ?? "render",
          caption: meta?.caption || site?.caption || undefined,
          width: meta?.width,
          height: meta?.height,
        };
      });
  return (
    <Show when={artifacts().length > 0}>
      <div
        class="den-visual-present"
        classList={{
          "den-visual-present--strip": artifacts().length >= 2,
        }}
        data-testid="visual-present-block"
      >
        <KeyedIndex each={artifacts()} keyOf={(a) => a.id}>
          {(artifact) => (
            <TranscriptVisualSlot
              artifact={artifact()}
              sessionId={props.sessionId}
              projectId={props.projectId}
              client={props.client}
              entryKey={visualPresentEntryKey(props.rowKey, artifact().id)}
              prominent
            />
          )}
        </KeyedIndex>
      </div>
    </Show>
  );
}
