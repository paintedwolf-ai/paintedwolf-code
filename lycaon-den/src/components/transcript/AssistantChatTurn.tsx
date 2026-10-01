import { Show, createMemo } from "solid-js";
import type { Message, WorkflowRun } from "../../api/types.ts";
import {
  draftVersionCount,
  isCoordinatorDraftVariantB,
} from "../../chat/transcript/projection/draft-model.ts";
import type { TranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import {
  buildProseCitationIndex,
  buildProseNavigationIndex,
} from "../../chat/markdown/prose-path-opens.ts";
import type { CitationExploreContext } from "../citation/CitationGroundingPanel.tsx";
import { VisualPresentBlock } from "./VisualPresentBlock.tsx";
import { AssistantProseBody } from "./AssistantProseBody.tsx";
import {
  REDACTION_FIELD_CONTENT,
  spansForField,
} from "../../chat/transcript/content/redaction-spans.ts";
import { CitationEvidenceChicklet } from "../citation/CitationEvidenceChicklet.tsx";
import { DraftRail } from "./DraftRail.tsx";
import { deriveReportAvailable } from "../../workflow/workflows-drawer-model.ts";
import { DownloadReportButton } from "../workflow/DownloadReportButton.tsx";
import type { AppStore } from "../../store/app-state-model.ts";
import { MessageActions } from "./MessageActions.tsx";

function resolveWorkflowRun(
  runId: string,
  store: AppStore | undefined | null,
): WorkflowRun | undefined {
  if (!store) return undefined;
  const fromList = store.state.workflowRuns.find((r) => r.id === runId);
  if (fromList) return fromList;
  const active = store.state.activeWorkflowRun;
  return active?.id === runId ? active : undefined;
}

type Props = {
  item: Extract<TranscriptItem, { kind: "assistant" }>;
  /** The host row behind the item; its grounding and provenance render beside the prose. */
  wireRow: () => Message | undefined;
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  streaming?: boolean;
  client?: import("../../api/client.ts").LycaonClient | null;
  appStore?: AppStore | null;
  exploreContext?: CitationExploreContext;
  onExploreInSearch?: (query: string) => void;
  onCopy?: (text: string) => void;
};

export function AssistantChatTurn(props: Props) {
  const rowKey = () => props.item.key;
  const content = () => props.item.text;
  const wireRow = createMemo(() => props.wireRow());
  const grounding = createMemo(() => wireRow()?.grounding);
  const proseCitations = createMemo(() => buildProseCitationIndex(grounding()));
  const proseNavigation = createMemo(() =>
    buildProseNavigationIndex(wireRow()?.navigation_refs),
  );
  // Apply wire offsets only when the item text matches the wire content.
  const proseRedactionSpans = createMemo(() => {
    const wire = wireRow();
    if (!wire || wire.content !== content()) return undefined;
    const spans = spansForField(wire, REDACTION_FIELD_CONTENT);
    return spans.length > 0 ? spans : undefined;
  });
  const isAgentNote = createMemo(
    () => wireRow()?.kind === "agent_note",
  );
  // The accepted answer controls its superseded variants.
  const showVersionRail = createMemo(() => {
    const msg = wireRow();
    if (!msg || isAgentNote()) return false;
    return isCoordinatorDraftVariantB(msg);
  });
  const versionCount = createMemo(() => {
    const msg = wireRow();
    return msg ? draftVersionCount(msg) : 1;
  });

  // The host authorizes downloads for run-scoped report documents.
  const reportDownload = createMemo(() => {
    const msg = wireRow();
    const client = props.client;
    if (!msg?.grounding || isAgentNote() || !client) return null;
    const scope = msg.completion_report?.scope;
    if (scope !== "run") return null;
    const runId = msg.workflow_run_id?.trim();
    if (!runId || !props.appStore) return null;
    const run = resolveWorkflowRun(runId, props.appStore);
    if (!run) return null;
    if (!deriveReportAvailable(run)) return null;
    return {
      runId,
      downloadReport: (id: string) => client.getWorkflowRunReport(id),
    };
  });

  const streaming = () =>
    props.streaming === true || wireRow()?.status === "streaming";

  return (
    <article
      class="assistant-turn"
      classList={{ "assistant-turn--note": isAgentNote() }}
      aria-label="Assistant"
      aria-busy={streaming() ? true : undefined}
      data-testid="transcript-article-assistant"
      data-kind={isAgentNote() ? "agent_note" : undefined}
    >
      <Show when={showVersionRail()}>
        <DraftRail
          sessionId={props.sessionId}
          slotId={rowKey()}
          versionCount={versionCount()}
          live={false}
          client={props.client}
        />
      </Show>
      <Show when={isAgentNote()}>
        <span
          class="assistant-turn__note-badge den-status-mark"
          aria-label="Note"
          data-testid="agent-note-badge"
        >
          Note
        </span>
      </Show>
      <div class="bubble bubble--assistant">
        <Show when={props.onCopy}>
          <MessageActions onCopy={() => props.onCopy?.(content())} />
        </Show>
        <Show when={(wireRow()?.artifact_ids?.length ?? 0) > 0}>
          <VisualPresentBlock
            artifactIds={wireRow()?.artifact_ids ?? []}
            sessionId={props.sessionId}
            projectId={props.projectId}
            client={props.client}
            rowKey={wireRow()?.id ?? rowKey()}
          />
        </Show>
        <AssistantProseBody
          client={props.client}
          messageId={wireRow()?.id}
          messageContent={wireRow()?.content}
          source={content()}
          redactionSpans={proseRedactionSpans()}
          sessionId={props.sessionId}
          projectId={props.projectId}
          citations={proseCitations()}
          navigation={proseNavigation()}
          rootRefs={props.rootRefs}
        />
        <Show when={!isAgentNote() && reportDownload()} keyed>
          {(dl) => (
            <DownloadReportButton
              runId={dl.runId}
              variant="transcript"
              downloadReport={dl.downloadReport}
            />
          )}
        </Show>
      </div>
      <Show when={grounding()}>
        <CitationEvidenceChicklet
          grounding={grounding()}
          projectId={props.projectId}
          rootRefs={props.rootRefs}
          exploreContext={props.exploreContext}
          onExplore={props.onExploreInSearch}
          sessionId={props.sessionId}
          rowKey={rowKey()}
        />
      </Show>
    </article>
  );
}
