import { Show, createEffect, createSignal, on, onCleanup } from "solid-js";
import type { WorkflowExplainMeta } from "../../api/types.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { bindFindRevealHost } from "../../find/use-findable-view.ts";
import { SCANS_LIVE_REFRESH_MAX_MS, SCANS_LIVE_REFRESH_MS } from "../../lib/scan-display.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { createAdaptivePoller } from "../../store/adaptive-poller.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import {
  topologyLegRows,
  workflowExplainLegs,
  workflowExplainPass,
  workflowExplainState,
  workflowExplainStatus,
  workflowExplainWantsLiveProgress,
} from "../../workflow/workflow-explain-model.ts";
import { ProgressRows } from "../primitives/ProgressRows.tsx";
import { createProjectSecurityQuery, type ProjectSecuritySource } from "../scan/project-security-query.ts";
import { ScanProgressList } from "../scan/ScanProgressList.tsx";
import { TranscriptChickletSummary } from "../transcript/TranscriptChickletSummary.tsx";

export const WORKFLOW_EXPLAIN_NAME = "About this step";

type Props = {
  meta: WorkflowExplainMeta;
  runId?: string;
  sessionId?: string | null;
  entryKey?: string;
  client?: ProjectSecuritySource["client"] | null;
  appStore?: AppStore | null;
};

/** The host's note for a phase it holds, with the progress of the record it names. */
export function WorkflowExplainChicklet(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.entryKey ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey } : undefined,
  );
  const { key: disclosureKey, open, onToggle, onSummaryClick, revealTemporarily } = useTranscriptDisclosure(() =>
    props.entryKey ? transcriptDisclosureKey.workflowExplain(props.entryKey) : undefined,
  );
  const [hostEl, setHostEl] = createSignal<HTMLElement | null>(null);
  const [summaryEl, setSummaryEl] = createSignal<HTMLElement | null>(null);

  const run = () => {
    const id = props.runId;
    return id ? props.appStore?.state.workflowRuns.find((candidate) => candidate.id === id) : undefined;
  };
  const status = () => workflowExplainStatus(props.meta, run());
  const live = () => workflowExplainWantsLiveProgress(props.meta, run());

  // Progress is read only while it can still move, or while someone reads it.
  const security = createProjectSecurityQuery(() => {
    const target = props.meta.progress?.full_pass;
    const client = props.client;
    if (!target || !client || !(live() || open())) return null;
    return { client, projectId: target.project_id, rootId: target.root_id };
  });
  const pass = () => workflowExplainPass(props.meta, security.value());
  // Legs arrive with the run, which a leg update refreshes.
  const legs = () => workflowExplainLegs(props.meta, run());
  const progressState = () => workflowExplainState(pass(), legs());

  const residentLive = useResidentLive();
  const poller = createAdaptivePoller(
    async () => {
      await security.refresh();
    },
    SCANS_LIVE_REFRESH_MS,
    SCANS_LIVE_REFRESH_MAX_MS,
  );
  onCleanup(() => poller.cancel());
  createEffect(() => poller.setEnabled(live() && residentLive()));
  createEffect(
    on(
      () => props.appStore?.state.latestCodeScan,
      (event) => {
        if (event && live() && residentLive()) void security.refresh();
      },
      { defer: true },
    ),
  );

  bindFindRevealHost({
    id: `workflow-explain:${props.entryKey ?? props.meta.phase_id}`,
    hostEl,
    isCollapsed: () => !open(),
    revealForFind: revealTemporarily,
    collapsedCorpus: () => `${props.meta.summary}\n${props.meta.body}`,
    collapsedCorpusAnchor: summaryEl,
  });

  return (
    <details
      ref={(el) => {
        setHostEl(el);
        bindTranscriptEntry(el);
      }}
      class="den-tool-part-card den-transcript-disclosure-card den-tool-part"
      data-testid="workflow-explain-chicklet"
      data-status={status()}
      data-phase-id={props.meta.phase_id}
      data-disclosure-key={disclosureKey}
      open={open()}
      onToggle={onToggle}
    >
      <TranscriptChickletSummary
        ref={setSummaryEl}
        onClick={onSummaryClick}
        label={[WORKFLOW_EXPLAIN_NAME, props.meta.summary, progressState()].filter(Boolean).join(", ")}
      >
        <span class="den-tool-part-status-dot" data-status={status()} aria-hidden="true" />
        <span class="den-tool-part-name">{WORKFLOW_EXPLAIN_NAME}</span>
        <span class="den-tool-part-title">{props.meta.summary}</span>
        <Show when={progressState()}>
          {(state) => (
            <span class="den-tool-part-title" data-testid="workflow-explain-state">
              {state()}
            </span>
          )}
        </Show>
      </TranscriptChickletSummary>
      <div class="den-tool-part-card-body den-tool-part-body" data-testid="workflow-explain-body">
        <Show when={open()}>
          <p class="den-tool-part-card-note">{props.meta.body}</p>
          <Show when={pass()} keyed>
            {(current) => (
              <ScanProgressList pass={current} overview={security.value() ?? null} testId="workflow-explain-progress" />
            )}
          </Show>
          <ProgressRows rows={topologyLegRows(legs(), run())} testId="workflow-explain-legs" />
        </Show>
      </div>
    </details>
  );
}
