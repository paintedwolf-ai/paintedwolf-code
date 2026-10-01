import { For, Show } from "solid-js";
import type { BoardView, FindingsDigest, Finding, WorkerTask } from "../../api/types.ts";
import {
  findingAgentLabel,
  findingsByWorker,
  reservationsFor,
  siblingNotesFor,
} from "../../chat/worker/worker-coordination-model.ts";
import { WorkerTranscriptSection } from "./WorkerTranscriptSection.tsx";

type Props = {
  worker: WorkerTask;
  findings?: FindingsDigest;
  roster?: BoardView["roster"];
  onSelect: () => void;
};

function FindingRows(props: {
  rows: Finding[];
  testId: string;
  roster?: BoardView["roster"];
  showAgent: boolean;
}) {
  return (
    <ul class="den-worker-transcript-coordination-list">
      <For each={props.rows}>
        {(finding) => {
          const agent = () =>
            props.showAgent ? findingAgentLabel(props.roster, finding.agent) : "";
          return (
            <li
              class="den-worker-transcript-coordination-finding"
              data-testid={props.testId}
            >
              <p class="den-worker-transcript-coordination-summary">
                {finding.summary}
              </p>
              <Show when={agent() || finding.ref}>
                <div class="den-worker-transcript-coordination-meta">
                  <Show when={agent()}>
                    {(label) => (
                      <span class="den-worker-transcript-coordination-agent">
                        {label()}
                      </span>
                    )}
                  </Show>
                  <Show when={finding.ref}>
                    <span class="den-worker-transcript-coordination-ref">
                      {finding.ref}
                    </span>
                  </Show>
                </div>
              </Show>
            </li>
          );
        }}
      </For>
    </ul>
  );
}

export function WorkerCoordinationSection(props: Props) {
  const jobId = () => props.worker.id;
  const posted = () => findingsByWorker(props.findings, jobId());
  const received = () =>
    siblingNotesFor(props.findings, jobId(), props.worker.created_at);
  const reserved = () => reservationsFor(props.roster, jobId());
  const isEmpty = () =>
    posted().length === 0 && received().length === 0 && reserved().length === 0;

  return (
    <WorkerTranscriptSection
      title="Coordination"
      sectionKey="coordination"
      class="den-worker-transcript-coordination"
      data-testid="worker-coordination"
      status="complete"
      onSelect={() => props.onSelect()}
    >
      <Show
        when={!isEmpty()}
        fallback={
          <p class="den-worker-transcript-empty den-worker-transcript-empty--inline">
            No coordination activity yet — notes and reservations appear here.
          </p>
        }
      >
        <Show when={posted().length > 0}>
          <section class="den-worker-transcript-coordination-group">
            <h4 class="den-worker-transcript-coordination-heading">Notes posted</h4>
            <FindingRows
              rows={posted()}
              testId="worker-coordination-posted"
              showAgent={false}
            />
          </section>
        </Show>
        <Show when={received().length > 0}>
          <section class="den-worker-transcript-coordination-group">
            <h4 class="den-worker-transcript-coordination-heading">Notes received</h4>
            <FindingRows
              rows={received()}
              testId="worker-coordination-received"
              roster={props.roster}
              showAgent
            />
          </section>
        </Show>
        <Show when={reserved().length > 0}>
          <section class="den-worker-transcript-coordination-group">
            <h4 class="den-worker-transcript-coordination-heading">Reservations</h4>
            <ul class="den-worker-transcript-coordination-list">
              <For each={reserved()}>
                {(path) => (
                  <li
                    class="den-worker-transcript-coordination-path"
                    data-testid="worker-coordination-reservation"
                  >
                    {path}
                  </li>
                )}
              </For>
            </ul>
          </section>
        </Show>
      </Show>
    </WorkerTranscriptSection>
  );
}
