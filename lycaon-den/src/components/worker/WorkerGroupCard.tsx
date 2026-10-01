import type { JSX } from "solid-js";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import {
  useTranscriptEntry,
  workerGroupEnterFadeKey,
} from "../../chat/transcript/presentation/transcript-entry.ts";
import { KeyedIndex } from "../keyed-index.tsx";

type Props = {
  parts: readonly ToolPartView[];
  sessionId?: string;
  entryKey: string;
  renderWorkerEntry: (part: () => ToolPartView) => JSX.Element;
};

export function WorkerGroupCard(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId,
    entryKey: workerGroupEnterFadeKey(props.entryKey),
  }));

  return (
    <section
      ref={bindTranscriptEntry}
      class="den-worker-group"
      data-testid="worker-group-card"
      aria-label={`${props.parts.length} ${props.parts.length === 1 ? "worker" : "workers"}`}
    >
      <header class="den-worker-group-header">
        <span class="den-worker-group-title">Workers</span>
        <span class="den-worker-group-count">×{props.parts.length}</span>
      </header>
      <div class="den-worker-group-list">
        <KeyedIndex each={props.parts} keyOf={(part) => part.id}>
          {(part) => (
            <div class="den-worker-group-item">
              {props.renderWorkerEntry(part)}
            </div>
          )}
        </KeyedIndex>
      </div>
    </section>
  );
}
