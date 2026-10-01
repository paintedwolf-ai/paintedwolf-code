import { For, Show } from "solid-js";
import type { BoardView, FindingsDigest, Message, WorkerTask } from "../../api/types.ts";
import {
  type LoadState,
  errorOf,
  isLoaded,
  valueOf,
} from "../../store/load-state.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { progressHistoryFromMessages } from "../../chat/progress/progress-model.ts";
import {
  reservationsByWorker,
  type WorklogReservationGroup,
} from "../../chat/worker/worklog-coordination-model.ts";
import { pendingWorkerApprovals } from "../../chat/worker/worker-approval-model.ts";
import { pinDrawerTranscriptSection } from "../../shell/drawer-transcript-scroll.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { ContextDrawer } from "../shell/ContextDrawer.tsx";
import { WorkerTranscriptSection } from "../worker/WorkerTranscriptSection.tsx";

export type WorklogPanelProps = {
  open: boolean;
  findings: LoadState<FindingsDigest | null>;
  board?: BoardView;
  workers?: readonly WorkerTask[];
  messages?: readonly Message[];
  pendingCheckpoints?: readonly PendingCheckpoint[];
  onOpenWorkerCheckpoint?: (workerId: string, checkpointId: string) => void;
  onClose: () => void;
};

const WORKLOG_SECTIONS = [
  "progress",
  "approvals",
  "findings",
  "reservations",
] as const;

function progressStateLabel(state: string): string {
  return state === "na" ? "Not applicable" : formatSentenceCase(state);
}

type WorklogSectionKey = (typeof WORKLOG_SECTIONS)[number];

export function WorklogPanel(props: WorklogPanelProps) {
  const findings = () => valueOf(props.findings)?.findings ?? [];
  const findingsSettled = () => isLoaded(props.findings);
  const findingsError = () => errorOf(props.findings);
  const reservations = () => reservationsByWorker(props.board);
  const progressLog = () => progressHistoryFromMessages(props.messages ?? []);
  const approvals = () =>
    pendingWorkerApprovals(props.workers ?? [], props.pendingCheckpoints ?? []);
  let paneEl: HTMLDivElement | undefined;

  const selectSection = (key: WorklogSectionKey) => {
    void pinDrawerTranscriptSection({
      pane: paneEl,
      sectionKey: key,
      sectionIndex: WORKLOG_SECTIONS.indexOf(key),
    });
  };

  return (
    <ContextDrawer
      open={props.open}
      testId="worklog-panel"
      titleId="worklog-drawer-title"
      title="Worklog"
      closeLabel="Close worklog"
      onClose={() => props.onClose()}
    >
      <div
        class="den-worker-transcript-pane"
        ref={(el) => {
          paneEl = el;
        }}
      >
        <WorkerTranscriptSection
          title="Progress"
          sectionKey="progress"
          onSelect={() => selectSection("progress")}
        >
          <Show
            when={progressLog().length > 0}
            fallback={
              <p class="den-worker-transcript-empty den-worker-transcript-empty--inline">
                No progress changes yet.
              </p>
            }
          >
            <ul class="worklog-panel__progress">
              <For each={progressLog()}>
                {(entry) => (
                  <li
                    class="worklog-panel__progress-row"
                    data-testid="worklog-progress-row"
                    data-kind={entry.kind}
                    data-state={entry.state}
                  >
                    <span class="worklog-panel__progress-kind">
                      {formatSentenceCase(entry.kind)}
                    </span>
                    <div class="worklog-panel__progress-body">
                      <p class="worklog-panel__progress-label">{entry.label}</p>
                      <p class="worklog-panel__progress-meta">
                        <span class="worklog-panel__progress-state">
                          {progressStateLabel(entry.state)}
                        </span>
                        <Show when={entry.ts}>
                          <span class="worklog-panel__progress-ref">{entry.ts}</span>
                        </Show>
                      </p>
                    </div>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </WorkerTranscriptSection>

        <WorkerTranscriptSection
          title="Approvals"
          sectionKey="approvals"
          onSelect={() => selectSection("approvals")}
        >
          <Show
            when={approvals().length > 0}
            fallback={
              <p class="den-worker-transcript-empty den-worker-transcript-empty--inline">
                No workers need approval.
              </p>
            }
          >
            <ul class="worklog-panel__approvals">
              <For each={approvals()}>
                {(approval) => (
                  <li
                    class="worklog-panel__approval"
                    data-testid="worklog-worker-approval"
                  >
                    <button
                      type="button"
                      class="worklog-panel__approval-link"
                      onClick={() =>
                        props.onOpenWorkerCheckpoint?.(
                          approval.jobId,
                          approval.checkpointId,
                        )
                      }
                    >
                      <span class="worklog-panel__approval-label">
                        Worker needs approval
                      </span>
                      <span class="worklog-panel__approval-command">
                        {approval.commandSummary}
                      </span>
                    </button>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </WorkerTranscriptSection>

        <WorkerTranscriptSection
          title="Findings"
          sectionKey="findings"
          onSelect={() => selectSection("findings")}
        >
          <Show when={findingsSettled() && findings().length === 0}>
            <p class="den-worker-transcript-empty den-worker-transcript-empty--inline">
              No worker findings yet.
            </p>
          </Show>
          <Show when={findingsError() !== undefined && findings().length === 0}>
            <p
              class="den-worker-transcript-empty den-worker-transcript-empty--inline"
              data-testid="worklog-findings-load-error"
            >
              Couldn’t load findings. They’ll refresh with the next update.
            </p>
          </Show>
          <Show when={findings().length > 0}>
            <ul class="worklog-panel__findings">
              <For each={findings()}>
                {(finding) => (
                  <li class="worklog-panel__finding" data-testid="worklog-finding-row">
                    <span class="worklog-panel__finding-agent">{finding.agent}</span>
                    <div class="worklog-panel__finding-body">
                      <p class="worklog-panel__finding-summary">{finding.summary}</p>
                      <Show when={finding.body}>
                        <details>
                          <summary><span class="den-disclosure-caret" aria-hidden="true" />Details</summary>
                          <p class="worklog-panel__finding-summary worklog-panel__finding-detail">{finding.body}</p>
                        </details>
                      </Show>
                      <Show when={finding.ref}>
                        <p class="worklog-panel__finding-ref">{finding.ref}</p>
                      </Show>
                      <Show when={finding.recorded_at}>
                        <p class="worklog-panel__finding-recorded_at">{finding.recorded_at}</p>
                      </Show>
                    </div>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </WorkerTranscriptSection>

        <WorkerTranscriptSection
          title="Reservations"
          sectionKey="reservations"
          onSelect={() => selectSection("reservations")}
        >
          <Show
            when={reservations().length > 0}
            fallback={
              <p class="den-worker-transcript-empty den-worker-transcript-empty--inline">
                No path reservations yet.
              </p>
            }
          >
            <ul class="worklog-panel__reservations">
              <For each={reservations()}>
                {(group: WorklogReservationGroup) => (
                  <li
                    class="worklog-panel__reservation-group"
                    data-testid="worklog-reservation-group"
                  >
                    <p class="worklog-panel__reservation-head">
                      <span class="worklog-panel__reservation-agent">
                        {group.agentType}
                      </span>
                      <span class="worklog-panel__reservation-job">{group.jobId}</span>
                    </p>
                    <ul class="worklog-panel__reservation-paths">
                      <For each={group.paths}>
                        {(path) => (
                          <li
                            class="worklog-panel__reservation-path"
                            data-testid="worklog-reservation-path"
                          >
                            {path}
                          </li>
                        )}
                      </For>
                    </ul>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </WorkerTranscriptSection>
      </div>
    </ContextDrawer>
  );
}
