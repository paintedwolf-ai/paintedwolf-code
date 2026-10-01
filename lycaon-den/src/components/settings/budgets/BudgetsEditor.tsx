import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";
import { createEffect, createMemo, Show } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { SettingsLimitsPatch } from "../../../api/types.ts";
import { createLimitsDraft } from "../../../settings/budgets/limits-model.ts";
import { createSettingsAutoSave } from "../../../settings/settings-auto-save.ts";
import { SETTINGS_EDITOR_COPY } from "../../../settings/editor/settings-editor-copy.ts";
import {
  SPEND_CEILING_WARN_RATIO,
  spendCeilingReadout,
} from "../../../settings/budgets/spend-ceiling-readout.ts";
import { NANO_PER_USD } from "../../../cost/nano-usd.ts";
import type { CostStore } from "../../../store/cost-store.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenNumberInput } from "../../primitives/DenNumberInput.tsx";
import { SettingsEditorTitle, BudgetsIcon } from "../SettingsEditorTitle.tsx";
import { SettingsGovernedGroup } from "../SettingsGovernedGroup.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";

type NumericLimitKey = { [K in keyof SettingsLimitsPatch]-?: NonNullable<SettingsLimitsPatch[K]> extends number ? K : never }[keyof SettingsLimitsPatch];

type Props = {
  client: LycaonClient;
  settingsStore: SettingsStore;
  costStore?: CostStore;
  embedded?: boolean;
};

export function BudgetsEditor(props: Props) {
  const draftState = createLimitsDraft(() => props.settingsStore.state.limits);
  const draft = draftState.value;

  const autoSave = createSettingsAutoSave({
    isReady: () => draft() != null,
    isDirty: draftState.dirty,
    save: async () => {
      const body = draftState.beginSave();
      props.settingsStore.actions.setError(undefined);
      try {
        const updated = await props.client.updateLimitsSettings(body);
        draftState.acknowledge(body);
        props.settingsStore.actions.setLimits(updated);
      } catch (err) {
        props.settingsStore.actions.setError(
          err instanceof Error ? err.message : String(err),
        );
      } finally {
        draftState.finishSave();
      }
    },
  });

  createEffect(() => {
    draft();
    autoSave.schedule();
  });

  const patchNumber = (key: NumericLimitKey, raw: string) => {
    const n = Number(raw);
    if (!Number.isFinite(n)) return;
    draftState.edit(key, n);
  };

  const patchBool = (key: "spend_ceiling_enabled" | "spend_soft_stop" | "coordinator_loop", checked: boolean) => {
    draftState.edit(key, checked);
  };

  const ceilingReadout = createMemo(() => {
    const body = draft();
    if (!body) {
      return spendCeilingReadout({
        enabled: false,
        ceilingUsd: 0,
        summary: undefined,
      });
    }
    return spendCeilingReadout({
      enabled: body.spend_ceiling_enabled === true,
      ceilingUsd: (body.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD,
      warningRatio: body.spend_warning_ratio ?? SPEND_CEILING_WARN_RATIO,
      summary: props.costStore?.state.session,
    });
  });

  return (
    <div
      class="den-settings-editor"
      classList={{ "den-settings-editor--embedded": props.embedded === true }}
      data-testid="budgets-editor"
    >
      <Show when={!props.embedded}>
        <SettingsEditorTitle icon={<BudgetsIcon />}>Budgets</SettingsEditorTitle>
      </Show>
      <ShowLatest when={draft()}>
        {(limitsSnap) => (
          <>
            <section
              class="den-settings-section"
              data-testid="spend-ceiling-section"
              {...settingAnchor("spend-ceiling")}
            >
              <h3 class="den-settings-subhead" {...chromeProps()}>{settingLabel("spend-ceiling")}</h3>
              <DenCheckbox
                checked={limitsSnap().spend_ceiling_enabled === true}
                data-testid="spend-ceiling-enabled"
                onChange={(e) =>
                  patchBool("spend_ceiling_enabled", e.currentTarget.checked)
                }
              >
                Enable spend safety ceiling
              </DenCheckbox>
              <SettingsGovernedGroup
                label="Spend safety ceiling limits"
                active={limitsSnap().spend_ceiling_enabled === true}
              >
              <DenField label={settingLabel("spend-ceiling-usd")} {...settingAnchor("spend-ceiling-usd")}>
                <DenNumberInput
                  min={0}
                  step="0.01"
                  value={(limitsSnap().session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD}
                  data-testid="spend-ceiling-usd"
                  onInput={(e) => {
                    const val = Number(e.currentTarget.value);
                    if (!Number.isFinite(val)) return;
                    draftState.edit("session_spend_ceiling_nano_usd", Math.round(val * NANO_PER_USD));
                  }}
                />
              </DenField>
              <DenField label={settingLabel("spend-warning")} {...settingAnchor("spend-warning")}>
                <DenNumberInput
                  min={10}
                  max={95}
                  step={1}
                  value={Math.round(
                    (limitsSnap().spend_warning_ratio ?? SPEND_CEILING_WARN_RATIO) *
                      100,
                  )}
                  data-testid="spend-warning-percent"
                  onInput={(e) => {
                    const percent = Number(e.currentTarget.value);
                    if (!Number.isFinite(percent)) return;
                    draftState.edit("spend_warning_ratio", percent / 100);
                  }}
                />
              </DenField>
              <DenCheckbox
                checked={limitsSnap().spend_soft_stop ?? true}
                data-testid="spend-soft-stop"
                onChange={(e) =>
                  patchBool("spend_soft_stop", e.currentTarget.checked)
                }
              >
                Allow a soft landing at the ceiling
              </DenCheckbox>
              <p class="den-settings-hint">
                On by default. A running session gets one bounded round to finish
                work already in flight or return its best usable result. If it uses
                tools, the host follows with a prose-only closeout. New prompts and
                workers still stop at the ceiling. A safety brake, not a budget.
              </p>
              </SettingsGovernedGroup>
              <SpendCeilingReadoutView readout={ceilingReadout()} />
            </section>

            <section class="den-settings-section">
              <h3 class="den-settings-subhead" {...chromeProps()}>Prompt loop</h3>
              <DenField label={settingLabel("max-iterations")} {...settingAnchor("max-iterations")}>
                <DenNumberInput
                  min={1}
                  value={limitsSnap().max_iterations ?? undefined}
                  onInput={(e) =>
                    patchNumber("max_iterations", e.currentTarget.value)
                  }
                />
              </DenField>
              <DenField label={settingLabel("overlay-promote-iterations")} {...settingAnchor("overlay-promote-iterations")}>
                <DenNumberInput
                  min={1}
                  value={limitsSnap().overlay_promote_max_iterations ?? undefined}
                  onInput={(e) =>
                    patchNumber(
                      "overlay_promote_max_iterations",
                      e.currentTarget.value,
                    )
                  }
                />
              </DenField>
              <p class="den-settings-hint">
                Iteration cap for coordinator overlay promote surface.
              </p>
            </section>

            <section class="den-settings-section">
              <h3 class="den-settings-subhead" {...chromeProps()}>Tool results</h3>
              <DenField label={settingLabel("max-tool-result-bytes")} {...settingAnchor("max-tool-result-bytes")}>
                <DenNumberInput
                  min={1024}
                  value={limitsSnap().max_tool_result_bytes ?? undefined}
                  onInput={(e) =>
                    patchNumber("max_tool_result_bytes", e.currentTarget.value)
                  }
                />
              </DenField>
            </section>

            <section class="den-settings-section" {...settingAnchor("coordinator-loop")}>
              <h3 class="den-settings-subhead" {...chromeProps()}>{settingLabel("coordinator-loop")}</h3>
              <DenCheckbox
                checked={limitsSnap().coordinator_loop !== false}
                data-testid="coordinator-loop-enabled"
                onChange={(e) =>
                  patchBool("coordinator_loop", e.currentTarget.checked)
                }
              >
                Host auto-prompt after worker legs
              </DenCheckbox>
              <SettingsGovernedGroup
                label="Coordinator loop cap"
                active={limitsSnap().coordinator_loop !== false}
              >
              <DenField label={settingLabel("coordinator-loop-cycles")} {...settingAnchor("coordinator-loop-cycles")}>
                <DenNumberInput
                  min={0}
                  value={limitsSnap().max_coordinator_loop_cycles ?? 0}
                  data-testid="coordinator-loop-cycles"
                  onInput={(e) =>
                    patchNumber(
                      "max_coordinator_loop_cycles",
                      e.currentTarget.value,
                    )
                  }
                />
              </DenField>
              <p class="den-settings-hint">
                Per workflow run cap on coordinator loop wake cycles.
              </p>
              </SettingsGovernedGroup>
            </section>

            <section class="den-settings-section">
              <h3 class="den-settings-subhead" {...chromeProps()}>Worker tool budget</h3>
              <DenField label={settingLabel("worker-budget-default")} {...settingAnchor("worker-budget-default")}>
                <DenNumberInput
                  min={limitsSnap().worker_tool_budget_min}
                  max={limitsSnap().worker_tool_budget_max}
                  value={limitsSnap().worker_tool_budget_default ?? undefined}
                  onInput={(e) =>
                    patchNumber("worker_tool_budget_default", e.currentTarget.value)
                  }
                />
              </DenField>
              <DenField label={settingLabel("worker-budget-min")} {...settingAnchor("worker-budget-min")}>
                <DenNumberInput
                  min={2}
                  value={limitsSnap().worker_tool_budget_min ?? undefined}
                  onInput={(e) =>
                    patchNumber("worker_tool_budget_min", e.currentTarget.value)
                  }
                />
              </DenField>
              <DenField label={settingLabel("worker-budget-max")} {...settingAnchor("worker-budget-max")}>
                <DenNumberInput
                  min={limitsSnap().worker_tool_budget_min}
                  value={limitsSnap().worker_tool_budget_max ?? undefined}
                  onInput={(e) =>
                    patchNumber("worker_tool_budget_max", e.currentTarget.value)
                  }
                />
              </DenField>
              <p class="den-settings-hint">
                Every worker starts at the default unless its task or planned
                leg sets a ceiling. A worker that needs more rounds asks its
                coordinator, which can raise the ceiling up to the maximum.
              </p>
            </section>

            <section class="den-settings-section">
              <h3 class="den-settings-subhead" {...chromeProps()}>Wall-clock timeouts</h3>
              <p class="den-settings-hint">
                All values are seconds. These caps apply per operation, not per
                session.
              </p>
              <DenField label={settingLabel("llm-turn-timeout")} {...settingAnchor("llm-turn-timeout")}>
                <DenNumberInput
                  min={1}
                  value={Math.round((limitsSnap().llm_turn_timeout_ms ?? 0) / 1000)}
                  onInput={(e) => {
                    const val = Number(e.currentTarget.value);
                    if (!Number.isFinite(val)) return;
                    draftState.edit("llm_turn_timeout_ms", Math.round(val * 1000));
                  }}
                />
              </DenField>
              <DenField label={settingLabel("coordinator-turn-timeout")} {...settingAnchor("coordinator-turn-timeout")}>
                <DenNumberInput
                  min={1}
                  value={Math.round((limitsSnap().coordinator_host_turn_timeout_ms ?? 0) / 1000)}
                  onInput={(e) => {
                    const val = Number(e.currentTarget.value);
                    if (!Number.isFinite(val)) return;
                    draftState.edit("coordinator_host_turn_timeout_ms", Math.round(val * 1000));
                  }}
                />
              </DenField>
              <DenField label={settingLabel("coordinator-max-sleep")} {...settingAnchor("coordinator-max-sleep")}>
                <DenNumberInput
                  min={1}
                  value={Math.round((limitsSnap().coordinator_max_sleep_ms ?? 0) / 1000)}
                  onInput={(e) => {
                    const val = Number(e.currentTarget.value);
                    if (!Number.isFinite(val)) return;
                    draftState.edit("coordinator_max_sleep_ms", Math.round(val * 1000));
                  }}
                />
              </DenField>
              <DenField label={settingLabel("await-workers-timeout")} {...settingAnchor("await-workers-timeout")}>
                <DenNumberInput
                  min={1}
                  value={Math.round((limitsSnap().await_parent_workers_timeout_ms ?? 0) / 1000)}
                  onInput={(e) => {
                    const val = Number(e.currentTarget.value);
                    if (!Number.isFinite(val)) return;
                    draftState.edit("await_parent_workers_timeout_ms", Math.round(val * 1000));
                  }}
                />
              </DenField>
            </section>
          </>
        )}
      </ShowLatest>
      <Show when={autoSave.saving()}>
        <p class="den-settings-hint" data-testid="budgets-saving">
          {SETTINGS_EDITOR_COPY.saving}
        </p>
      </Show>
    </div>
  );
}

function SpendCeilingReadoutView(props: {
  readout: ReturnType<typeof spendCeilingReadout>;
}) {
  const r = () => props.readout;
  return (
    <>
      <Show when={r().kind === "off"}>
        <p
          class="den-settings-hint"
          data-testid="spend-ceiling-readout"
          data-kind="off"
        >
          Off
        </p>
      </Show>
      <Show when={r().kind === "idle"}>
        <p
          class="den-settings-hint"
          data-testid="spend-ceiling-readout"
          data-kind="idle"
        >
          {(r() as { label: string }).label}
        </p>
      </Show>
      <Show when={r().kind === "unpriced"}>
        <p
          class="den-settings-hint den-settings-prefs-band__warn"
          role="status"
          data-testid="spend-ceiling-readout"
          data-kind="unpriced"
        >
          {(r() as { label: string }).label}
        </p>
      </Show>
      <Show when={r().kind === "priced"}>
        <div
          class="spend-ceiling-readout"
          classList={{
            "spend-ceiling-readout--near":
              (r() as { ratio: number; warningRatio?: number }).ratio >=
                ((r() as { warningRatio?: number }).warningRatio ??
                  SPEND_CEILING_WARN_RATIO),
          }}
          data-testid="spend-ceiling-readout"
          data-kind="priced"
          data-ratio={(r() as { ratio: number }).ratio.toFixed(2)}
        >
          <p class="den-settings-hint">{(r() as { label: string }).label}</p>
          <div class="spend-ceiling-readout__bar" aria-hidden="true">
            <span
              class="spend-ceiling-readout__fill"
              style={{
                width: `${Math.round((r() as { ratio: number }).ratio * 100)}%`,
              }}
            />
          </div>
        </div>
      </Show>
    </>
  );
}
