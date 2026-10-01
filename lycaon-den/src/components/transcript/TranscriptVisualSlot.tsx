import { Show } from "solid-js";
import type { VisualArtifact } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { useArtifactDedup } from "../../chat/visual/artifact-dedup-context.tsx";
import {
  artifactFaceFromEntryKey,
  visualProducerEntryKey,
} from "../../chat/visual/visual-artifact-canonical.ts";
import { ArtifactReferenceChip } from "./ArtifactReferenceChip.tsx";
import { TranscriptVisualArtifact } from "./TranscriptVisualArtifact.tsx";

type Props = {
  artifact: VisualArtifact;
  sessionId?: string | null;
  projectId?: string;
  client?: LycaonClient | null;
  /** Defaults to producer `visual:{id}` when omitted. */
  entryKey?: string;
  prominent?: boolean;
  toolOutput?: string | null;
};

export function TranscriptVisualSlot(props: Props) {
  const dedup = useArtifactDedup();
  const entryKey = () =>
    props.entryKey?.trim() || visualProducerEntryKey(props.artifact.id);
  const face = () =>
    artifactFaceFromEntryKey(entryKey(), props.artifact.id) ?? "producer";
  const showFull = () => {
    if (!dedup) return true;
    return dedup.isCanonicalEntry(props.artifact.id, entryKey());
  };

  return (
    <Show
      when={showFull()}
      fallback={
        <ArtifactReferenceChip
          artifactId={props.artifact.id}
          face={face()}
          caption={props.artifact.caption}
          source={props.artifact.source}
        />
      }
    >
      <TranscriptVisualArtifact
        artifact={props.artifact}
        sessionId={props.sessionId}
        projectId={props.projectId}
        client={props.client}
        entryKey={entryKey()}
        prominent={props.prominent}
        toolOutput={props.toolOutput}
      />
    </Show>
  );
}
