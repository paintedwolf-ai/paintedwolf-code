import { For, Show } from "solid-js";
import type {
  BlueprintSummary,
  WorkflowRun,
  WorkflowSummary,
} from "../../api/types.ts";
import {
  canArmCatalogWorkflow,
  deriveReportAvailable,
  deriveWorkflowPanelActions,
  blueprintNameForRun,
  sortWorkflowCatalog,
  workflowPickerRowTitle,
  workflowPickerRowTrigger,
  workflowPickerToggleLabel,
  workflowRunHeader,
  type WorkflowPanelAction,
} from "../../workflow/workflows-drawer-model.ts";
import { DownloadReportButton } from "./DownloadReportButton.tsx";
import { DenButton } from "../primitives/DenButton.tsx";

type Props = {
  activeRun: WorkflowRun | null | undefined;
  catalog: WorkflowSummary[];
  blueprintsById: Record<string, BlueprintSummary>;
  pickerOpen: boolean;
  error: string | null;
  busy: boolean;
  onExit: (run: WorkflowRun) => void;
  onPause: () => void;
  onResume: () => void;
  onAdvance: () => void;
  onReviewInChat: () => void;
  onOpenPicker: () => void;
  onClosePicker: () => void;
  /** Arm a catalog workflow — start on the next composer send. */
  onArmWorkflow: (workflow: WorkflowSummary) => void;
  onJumpToRun: (run: WorkflowRun) => void;
  /** When set, Download report appears for terminal report-enabled runs. */
  downloadReport?: (runId: string) => Promise<{ blob: Blob; filename: string }>;
};

function invokePanelAction(
  action: WorkflowPanelAction,
	run: WorkflowRun,
  props: Pick<
    Props,
    "onPause" | "onResume" | "onAdvance" | "onReviewInChat" | "onExit"
  >,
): void {
  switch (action.id) {
    case "pause":
      props.onPause();
      break;
    case "resume":
      props.onResume();
      break;
    case "advance":
      props.onAdvance();
      break;
    case "review":
      props.onReviewInChat();
      break;
    case "leave":
		props.onExit(run);
      break;
  }
}

export function WorkflowsTab(props: Props) {
  const active = () => props.activeRun ?? null;
  const canArm = () => {
    const run = active();
    return canArmCatalogWorkflow(run, run ? [run] : [], props.catalog);
  };
  const showPicker = () => props.pickerOpen && canArm();
  const pickerRows = () => sortWorkflowCatalog(props.catalog);

  const header = () => {
    const run = active();
    if (!run) return null;
    return workflowRunHeader(run, props.catalog, blueprintNameForRun(run, props.blueprintsById));
  };

  const actions = () => {
    const run = active();
    if (!run) return null;
    return deriveWorkflowPanelActions(run);
  };

  const reportAvailable = () => {
    const run = active();
    if (!run || !props.downloadReport) return false;
    return deriveReportAvailable(run);
  };

  return (
    <div class="den-workflows-tab" data-testid="workflows-tab">
      <Show when={props.error}>
        <p class="den-workflows-tab__error" role="alert">
          {props.error}
        </p>
      </Show>

      <Show when={active()} keyed>
        {(run: WorkflowRun) => (
          <section
            class="den-workflows-tab__zone den-workflows-tab__zone--active"
            data-testid="workflow-active-row"
          >
            <button
              type="button"
              class="den-workflows-tab__run-head"
              onClick={() => props.onJumpToRun(run)}
            >
              <div class="den-workflows-tab__run-title">
                <span
                  class="den-workflows-tab__status"
                  data-status={run.status}
                  aria-hidden="true"
                />
                <span class="den-workflows-tab__run-name">{header()?.name}</span>
                <span
                  class="den-workflows-tab__status-pill"
                  data-status={run.status}
                  data-testid="workflow-status-pill"
                >
                  {header()?.statusLabel}
                </span>
              </div>
            </button>

            <Show
              when={header()?.stepProgress}
              fallback={
                <Show when={header()?.phaseLabel}>
                  <div class="den-workflows-tab__phase-label">{header()?.phaseLabel}</div>
                </Show>
              }
            >
              {(progress) => (
                <div class="den-workflows-tab__phase" data-testid="workflow-phase-progress">
                  <div class="den-workflows-tab__phase-bar" aria-hidden="true">
                    <For each={Array.from({ length: progress().total })}>
                      {(_, i) => (
                        <span
                          class="den-workflows-tab__phase-seg"
                          data-done={i() < progress().index ? "" : undefined}
                        />
                      )}
                    </For>
                  </div>
                  <div class="den-workflows-tab__phase-label">
                    {header()?.phaseLabel} · step {progress().index} of {progress().total}
                  </div>
                </div>
              )}
            </Show>

            <Show when={header()?.stateHint}>
              {(hint) => (
                <p class="den-workflows-tab__hint" data-testid="workflow-state-hint">
                  {hint()}
                </p>
              )}
            </Show>

            <Show when={actions()} keyed>
              {(set) => (
                <Show
                  when={set.review}
                  fallback={
                    <div class="den-workflows-tab__actions">
                      <For each={set.controls}>
                        {(action) => (
                          <DenButton
                            variant="secondary"
                            compact
                            disabled={props.busy}
                            data-testid={action.testId}
							onClick={() => invokePanelAction(action, run, props)}
                          >
                            {action.label}
                          </DenButton>
                        )}
                      </For>
                      <Show when={reportAvailable() ? props.downloadReport : undefined} keyed>
                        {(download) => (
                          <DownloadReportButton
                            runId={run.id}
                            downloadReport={download}
                            disabled={props.busy}
                          />
                        )}
                      </Show>
                      <span class="den-workflows-tab__actions-spacer" />
                      <DenButton
                        variant="danger"
                        compact
                        disabled={props.busy}
                        data-testid={set.leave.testId}
						onClick={() => invokePanelAction(set.leave, run, props)}
                      >
                        {set.leave.label}
                      </DenButton>
                    </div>
                  }
                >
                  {(review) => (
                    <>
                      <button
                        type="button"
                        class="den-workflows-tab__gate"
                        disabled={props.busy}
                        data-testid="workflow-gate-review"
						onClick={() => invokePanelAction(review(), run, props)}
                      >
                        <span class="den-workflows-tab__gate-label">
                          Awaiting your approval
                        </span>
                        <span class="den-workflows-tab__gate-cta">{review().label}</span>
                      </button>
                      <div class="den-workflows-tab__actions">
                        <Show when={reportAvailable() ? props.downloadReport : undefined} keyed>
                          {(download) => (
                            <DownloadReportButton
                              runId={run.id}
                              downloadReport={download}
                              disabled={props.busy}
                            />
                          )}
                        </Show>
                        <span class="den-workflows-tab__actions-spacer" />
                        <DenButton
                          variant="danger"
                          compact
                          disabled={props.busy}
                          data-testid={set.leave.testId}
						  onClick={() => invokePanelAction(set.leave, run, props)}
                        >
                          {set.leave.label}
                        </DenButton>
                      </div>
                    </>
                  )}
                </Show>
              )}
            </Show>
          </section>
        )}
      </Show>

      <section class="den-workflows-tab__zone den-workflows-tab__zone--footer">
        <Show when={!active()}>
          <div class="den-workflows-tab__resting" data-testid="workflows-tab-empty">
            <span class="den-workflows-tab__status" aria-hidden="true" />
            <span class="den-workflows-tab__resting-label">No workflow running</span>
          </div>
        </Show>
        <DenButton
          variant="primary"
          compact
          disabled={!canArm() || props.busy}
          data-testid="pick-workflow-button"
          onClick={() => (props.pickerOpen ? props.onClosePicker() : props.onOpenPicker())}
        >
          {workflowPickerToggleLabel(props.pickerOpen)}
        </DenButton>
        <Show when={active() && !canArm()}>
          <span
            class="den-workflows-tab__hint"
            role="status"
            data-testid="workflows-arm-blocked-hint"
          >
            Leave the current workflow before picking another.
          </span>
        </Show>
      </section>

      <Show when={showPicker()}>
        <section
          class="den-workflows-tab__zone den-workflows-tab__zone--picker"
          data-testid="workflow-picker"
        >
          <Show
            when={pickerRows().length > 0}
            fallback={
              <span class="den-workflows-tab__hint" role="status">
                No workflows available.
              </span>
            }
          >
            <div class="den-workflows-tab__picker-menu">
              <For each={pickerRows()}>
                {(wf) => (
                  <button
                    type="button"
                    class="den-workflows-tab__picker-row"
                    disabled={props.busy}
                    data-testid={`workflow-picker-row-${wf.start_preset_id ?? wf.id}`}
                    onClick={() => props.onArmWorkflow(wf)}
                  >
                    <span class="den-workflows-tab__picker-row-title">
                      {workflowPickerRowTitle(wf)}
                    </span>
                    <Show when={workflowPickerRowTrigger(wf)}>
                      {(trigger) => (
                        <code class="den-workflows-tab__picker-row-trigger">{trigger()}</code>
                      )}
                    </Show>
                  </button>
                )}
              </For>
            </div>
          </Show>
        </section>
      </Show>
    </div>
  );
}
