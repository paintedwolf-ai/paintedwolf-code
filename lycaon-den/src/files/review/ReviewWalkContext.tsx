import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import { For, Show, createMemo, type Accessor } from "solid-js";
import type {  SourceWalkEffect } from "../../api/types.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { fileBasename, fileDirname, opGlyph } from "./review-model.ts";
import { wholeFileOpChange } from "../../components/source/reader/source-reader-change.ts";
import {
  buildReviewWalkDetails,
  reviewWalkFileProgress,
  type ReviewWalkDetails,
} from "./review-walk-details.ts";
import {
  commandWindowAdmissionLabel,
  commandWindowLine,
  commandWindowObservation,
  commandWindowStateNote,
} from "../source/source-command-window.ts";
import { gitChangeRefs } from "../source/source-git-change.ts";
import {
  walkStepActor,
  walkGroupFileSummary,
  walkGroupGlyph,
  walkObservedSpan,
  walkStepGitChange,
  walkStepOperation,
  type WalkCommandStep,
  type WalkGitStep,
  type WalkGroupStep,
  type WalkOutsideStep,
  type WalkStep,
} from "../walk/walk-model.ts";

type Props = {
  step: WalkStep | null;
  position: number;
  count: number;
  loading: boolean;
  ready: boolean;
  comparison: ComparisonSnapshot | null;
  walkSteps: readonly WalkStep[];
};

function shortCommit(commit: string | undefined): string {
  return commit?.trim().slice(0, 7) ?? "";
}

function gitCommitSpan(step: WalkGitStep): string {
  const from = shortCommit(step.change.from_commit);
  const to = shortCommit(step.change.to_commit);
  if (from && to) return `${from} → ${to}`;
  return to || from;
}

function lineCountLabel(before: number, after: number): string {
  const unit = after === 1 ? "line" : "lines";
  return before === after ? `${after} ${unit}` : `${before} → ${after} ${unit}`;
}

function lineCounts(
  details: ReviewWalkDetails,
): { before: number; after: number } | null {
  const before = details.beforeLines;
  const after = details.afterLines;
  return before === null || after === null ? null : { before, after };
}

export function ReviewWalkContext(props: Props) {
  const effect = createMemo(() =>
    props.step?.kind === "effect" ? props.step.effect : null
  );
  const details = createMemo(() => {
    const current = effect();
    return current ? buildReviewWalkDetails(current, props.comparison) : null;
  });
  const fileProgress = createMemo(() => {
    const step = props.step;
    return step ? reviewWalkFileProgress(step, props.walkSteps) : null;
  });
  const groupStep = createMemo<WalkGroupStep | null>(() => {
    const step = props.step;
    return step && step.kind !== "effect" ? step : null;
  });
  const gitStep = createMemo(() => {
    const step = groupStep();
    return step?.kind === "git" ? step : null;
  });
  const commandStep = createMemo(() => {
    const step = groupStep();
    return step?.kind === "command" ? step : null;
  });
  const outsideStep = createMemo(() => {
    const step = groupStep();
    return step?.kind === "outside" ? step : null;
  });
  const groupTip = createMemo<string | null>(() => {
    const command = commandStep();
    if (command) return commandWindowLine(command.command);
    const git = gitStep();
    if (git) return walkStepGitChange(git);
    const outside = outsideStep();
    return outside ? walkObservedSpan(outside.effects) : null;
  });

  const commandDetail = (command: Accessor<WalkCommandStep>) => (
    <>
      <p
        class="den-review-detail__summary"
        data-testid="review-walk-command-observation"
      >
        {commandWindowObservation()}
      </p>
      <Show when={commandWindowStateNote(command().command)}>
        {(note) => (
          <p
            class="den-review-detail__summary"
            data-testid="review-walk-command-state"
          >
            {note()}
          </p>
        )}
      </Show>
      <div class="den-review-detail__meta">
        <span>
          Command
          <Show when={relativeTimeLabel(command().command.started_at)}>
            {(time) => <> · {time()}</>}
          </Show>
        </span>
      </div>
      <dl
        class="den-files-info__meta"
        aria-label="Command details"
      >
        <div class="den-files-info__meta-row">
          <dt>Ran</dt>
          <dd
            class="min-w-0 break-all"
            data-testid="review-walk-command"
          >
            {commandWindowLine(command().command)}
          </dd>
        </div>
        <div class="den-files-info__meta-row">
          <dt>Tool</dt>
          <dd data-testid="review-walk-command-tool">
            {command().command.tool_name}
          </dd>
        </div>
        <Show when={command().command.turn > 0}>
          <div class="den-files-info__meta-row">
            <dt>Turn</dt>
            <dd data-testid="review-walk-command-turn">
              {command().command.turn}
            </dd>
          </div>
        </Show>
        <Show when={commandWindowAdmissionLabel(command().command)}>
          {(admission) => (
            <div class="den-files-info__meta-row">
              <dt>Counted</dt>
              <dd data-testid="review-walk-command-admission">
                {admission()}
              </dd>
            </div>
          )}
        </Show>
      </dl>
    </>
  );

  const gitDetail = (git: Accessor<WalkGitStep>) => (
    <>
      <Show when={git().change.detail?.trim()}>
        {(subject) => (
          <p
            class="den-review-detail__summary"
            data-testid="review-walk-git-detail"
          >
            {subject()}
          </p>
        )}
      </Show>
      <div class="den-review-detail__meta">
        <span>
          Git
          <Show when={relativeTimeLabel(git().change.observed_at)}>
            {(time) => <> · {time()}</>}
          </Show>
        </span>
      </div>
      <dl
        class="den-files-info__meta"
        aria-label="Git movement details"
      >
        <div class="den-files-info__meta-row">
          <dt>Git</dt>
          <dd
            class="min-w-0 break-all"
            data-testid="review-walk-git"
          >
            {walkStepGitChange(git())}
          </dd>
        </div>
        <Show when={gitCommitSpan(git())}>
          {(span) => (
            <div class="den-files-info__meta-row">
              <dt>Commit</dt>
              <dd data-testid="review-walk-git-commits">
                {span()}
              </dd>
            </div>
          )}
        </Show>
      </dl>
      <Show when={git().effects.length === 0}>
        <p
          class="den-review-detail__summary"
          data-testid="review-walk-git-bare"
        >
          Open the Git review to see the files recorded in this change.
        </p>
      </Show>
    </>
  );

  const outsideDetail = (outside: Accessor<WalkOutsideStep>) => (
    <>
      <p
        class="den-review-detail__summary"
        data-testid="review-walk-outside-observation"
      >
        Files that changed outside the app. Nothing in this chat wrote them.
      </p>
      <div class="den-review-detail__meta">
        <span>
          Outside the app
          <Show when={walkObservedSpan(outside().effects)}>
            {(span) => <> · {span()}</>}
          </Show>
        </span>
      </div>
    </>
  );

  // Accessors keep the mounted detail block aligned with the selected step.
  const effectDetail = (
    current: Accessor<WalkStep>,
    held: Accessor<SourceWalkEffect>,
  ) => (
    <Show when={details()}>
      {(detail) => (
        <>
          <p
            class="den-review-detail__summary"
            data-testid="review-walk-summary"
          >
            {detail().summary}
          </p>
          <div class="den-review-detail__meta">
            <span>
              {walkStepActor(current())}
              <Show when={relativeTimeLabel(held().observed_at)}>
                {(time) => <> · {time()}</>}
              </Show>
            </span>
          </div>
          <Show when={detail().availabilityNote}>
            {(note) => <p class="den-review-detail__summary">{note()}</p>}
          </Show>
          <dl class="den-files-info__meta" aria-label="Current step details">
            <div class="den-files-info__meta-row">
              <dt>Path</dt>
              <dd class="min-w-0 break-all" data-testid="review-walk-path">
                {held().path}
              </dd>
            </div>
            <Show when={held().from_path?.trim()}>
              {(fromPath) => (
                <div class="den-files-info__meta-row">
                  <dt>From</dt>
                  <dd class="min-w-0 break-all">{fromPath()}</dd>
                </div>
              )}
            </Show>
            <Show when={detail().added > 0 || detail().removed > 0}>
              <div class="den-files-info__meta-row">
                <dt>Change</dt>
                <dd data-testid="review-walk-stats">
                  <Show when={detail().added > 0}>
                    <span class="den-review-row__added">
                      +{detail().added}
                    </span>
                  </Show>{" "}
                  <Show when={detail().removed > 0}>
                    <span class="den-review-row__removed">
                      −{detail().removed}
                    </span>
                  </Show>
                </dd>
              </div>
            </Show>
            <Show when={lineCounts(detail())}>
              {(counts) => (
                <div class="den-files-info__meta-row">
                  <dt>File</dt>
                  <dd data-testid="review-walk-line-count">
                    {lineCountLabel(counts().before, counts().after)}
                  </dd>
                </div>
              )}
            </Show>
            <Show when={(fileProgress()?.count ?? 0) > 1}>
              <div class="den-files-info__meta-row">
                <dt>In this file</dt>
                <dd data-testid="review-walk-file-position">
                  Step {fileProgress()?.position} of {fileProgress()?.count}
                </dd>
              </div>
            </Show>
            <Show when={held().tool_name?.trim()}>
              {(tool) => (
                <div class="den-files-info__meta-row">
                  <dt>Tool</dt>
                  <dd data-testid="review-walk-tool">{tool()}</dd>
                </div>
              )}
            </Show>
            <Show when={held().turn > 0}>
              <div class="den-files-info__meta-row">
                <dt>Turn</dt>
                <dd data-testid="review-walk-turn">{held().turn}</dd>
              </div>
            </Show>
          </dl>

          <Show when={detail().areas.length > 0}>
            <p class="den-review-detail__summary">Changed areas</p>
            <ul
              class="den-review-steps"
              aria-label="Changed areas"
              data-testid="review-walk-areas"
            >
              <For each={detail().areas}>
                {(area) => (
                  <li class="den-review-detail__meta">
                    <span>{area.label}</span>
                    <span class="den-review-row__stat">
                      <Show when={area.added > 0}>
                        <span class="den-review-row__added">
                          +{area.added}
                        </span>
                      </Show>
                      <Show when={area.removed > 0}>
                        <span class="den-review-row__removed">
                          −{area.removed}
                        </span>
                      </Show>
                    </span>
                  </li>
                )}
              </For>
              <Show when={detail().remainingAreas > 0}>
                <li class="den-review-detail__meta">
                  {detail().remainingAreas} more in the editor
                </li>
              </Show>
            </ul>
          </Show>
        </>
      )}
    </Show>
  );

  return (
    <section
      class="den-review-walk-context den-review-lens__section"
      data-testid="review-walk-context"
      aria-live="polite"
      aria-atomic="true"
    >
      <div class="den-review-lens__header-row" data-files-ctx="no-menu">
        <h2 class="den-review-lens__scope-header">Current step</h2>
        <Show when={props.count > 0}>
          <span
            class="den-review-lens__total"
            data-testid="review-walk-position"
          >
            {props.position} of {props.count}
          </span>
        </Show>
      </div>

      <Show when={props.loading}>
        <div class="den-review-lens__status">
          <p class="den-review-lens__loading">Preparing step…</p>
        </div>
      </Show>

      <Show
        when={props.step}
        fallback={
          <Show when={props.ready}>
            <p class="den-review-lens__empty">This walk has no file steps.</p>
          </Show>
        }
      >
        {(current) => (
          <div class="den-review-lens__list">
            <div class="den-review-file">
              <div class="den-review-file__line">
                <Show
                  when={groupStep()}
                  fallback={
                    <Show when={effect()}>
                      {(held) => (
                        <div
                          class="den-review-row den-review-row--open"
                          data-testid="review-walk-step"
                          data-files-ctx="no-menu"
                          data-tip={held().path}
                        >
                          <span class="den-review-row__op" aria-hidden="true">
                            {opGlyph(held().op)}
                          </span>
                          <span class="den-review-row__identity">
                            <span class="den-review-row__base" data-file-change={wholeFileOpChange(held().op)}>
                              {fileBasename(held().path)}
                            </span>
                            <Show when={fileDirname(held().path)}>
                              {(directory) => (
                                <span class="den-review-row__dir">
                                  {directory()}
                                </span>
                              )}
                            </Show>
                          </span>
                          <span class="den-review-row__verb">
                            {walkStepOperation(current())}
                          </span>
                        </div>
                      )}
                    </Show>
                  }
                >
                  {(group) => (
                    <div
                      class="den-review-row den-review-row--open"
                      data-testid="review-walk-step"
                      data-files-ctx="no-menu"
                      data-tip={groupTip() ?? undefined}
                    >
                      <span class="den-review-row__op" aria-hidden="true">
                        {walkGroupGlyph(group())}
                      </span>
                      <span class="den-review-row__identity">
                        <span class="den-review-row__base">
                          {walkStepOperation(current())}
                        </span>
                        <Show when={gitStep()}>
                          {(git) => (
                            <Show when={gitChangeRefs(git().change)}>
                              {(refs) => (
                                <span class="den-review-row__dir">{refs()}</span>
                              )}
                            </Show>
                          )}
                        </Show>
                        <Show when={commandStep()}>
                          {(command) => (
                            <span class="den-review-row__dir">
                              {commandWindowLine(command().command)}
                            </span>
                          )}
                        </Show>
                      </span>
                      <span class="den-review-row__verb">
                        {walkGroupFileSummary(group())}
                      </span>
                    </div>
                  )}
                </Show>
              </div>

              <div class="den-review-detail" data-testid="review-walk-detail">
                <Show when={commandStep()}>
                  {(command) => commandDetail(command)}
                </Show>
                <Show when={gitStep()}>
                  {(git) => gitDetail(git)}
                </Show>
                <Show when={outsideStep()}>
                  {(outside) => outsideDetail(outside)}
                </Show>
                <Show when={groupStep()}>
                  <p class="den-review-detail__summary" data-testid="review-walk-page-hint">
                    The files are listed in the editor.
                  </p>
                </Show>
                <Show when={effect()}>
                  {(held) => effectDetail(current, held)}
                </Show>
              </div>
            </div>
          </div>
        )}
      </Show>
    </section>
  );
}
