import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  StandingWatch,
  WatchCondition,
  WatchGate,
} from "../../api/types.ts";
import {
  formatHostWatchError,
  isValidTimeZone,
} from "../../watches/watch-format.ts";
import {
  formatRunInTimeZone,
  previewNextRuns,
  type StandingWatchEditorSeed,
} from "../../watches/watch-schedule-preview.ts";
import {
  createProjectStandingWatchAction,
  keepSessionWatchAction,
  updateProjectStandingWatchAction,
} from "../../watches/watches-actions.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenNumberInput } from "../primitives/DenNumberInput.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { StandingWatchConditionFields } from "./StandingWatchConditionFields.tsx";

const TARGET_KIND_OPTIONS = [
  { value: "new_chat", label: "New chat each run" },
  { value: "existing_chat", label: "Existing chat" },
] as const;

const GATE_KIND_OPTIONS = [
  { value: "", label: "None" },
  { value: "git_ref", label: "Git ref changed" },
  { value: "file_change", label: "Files changed" },
] as const;

type Props = {
  projectId: string;
  seed: StandingWatchEditorSeed;
  existingWatch?: StandingWatch;
  client: () => LycaonClient | null;
  onSaved: (watch: StandingWatch) => void;
  onCancel: () => void;
};

export function StandingWatchEditor(props: Props) {
  const [name, setName] = createSignal("");
  const [prompt, setPrompt] = createSignal("");
  const [conditions, setConditions] = createSignal<WatchCondition[]>([]);
  const [gateKind, setGateKind] = createSignal<"" | "git_ref" | "file_change">("");
  const [gateTarget, setGateTarget] = createSignal("");
  const [minGapMinutes, setMinGapMinutes] = createSignal(15);
  const [targetKind, setTargetKind] = createSignal<"new_chat" | "existing_chat">("new_chat");
  const [targetSessionId, setTargetSessionId] = createSignal("");
  const [workflowId, setWorkflowId] = createSignal("default");
  const [modelId, setModelId] = createSignal("");
  const [spendUsd, setSpendUsd] = createSignal("2");
  const [allowUnpriced, setAllowUnpriced] = createSignal(false);
  const [dailyRunCap, setDailyRunCap] = createSignal(8);
  const [timeZone, setTimeZone] = createSignal("UTC");
  const [enableWatch, setEnableWatch] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal<string | null>(null);

  createEffect(() => {
    const s = props.seed;
    setName(s.name);
    setPrompt(s.prompt);
    setConditions(s.conditions.map((c) => ({ ...c })));
    setGateKind(s.gate?.kind ?? "");
    setGateTarget(s.gate?.kind === "git_ref" ? (s.gate.ref ?? "") : (s.gate?.path ?? ""));
    setMinGapMinutes(s.minGapMinutes);
    setTargetKind(s.targetKind);
    setTargetSessionId(s.targetSessionId);
    setWorkflowId(s.workflowId || "default");
    setModelId(s.modelId);
    setSpendUsd(String(s.runSpendLimitNanoUsd / 1_000_000_000));
    setAllowUnpriced(s.allowUnpriced);
    setDailyRunCap(s.dailyRunCap);
    setTimeZone(s.timeZone);
    setEnableWatch(Boolean(s.enableDisabledTakeover || props.existingWatch?.state === "disabled"));
    setError(null);
  });

  const zoneValid = createMemo(() => isValidTimeZone(timeZone()));
  const nextThreeRuns = createMemo(() => {
    if (!zoneValid()) return [];
    const anchorMs = props.existingWatch
      ? Date.parse(props.existingWatch.created_at)
      : Date.now();
    return previewNextRuns(conditions(), timeZone(), anchorMs, Date.now(), 3);
  });

  const buildGate = (): WatchGate | undefined => {
    const kind = gateKind();
    const val = gateTarget().trim();
    if (!kind || !val) return undefined;
    if (kind === "git_ref") return { kind: "git_ref", ref: val };
    return { kind: "file_change", path: val };
  };

  const handleSubmit = async (e: SubmitEvent) => {
    e.preventDefault();
    const client = props.client();
    if (!client || saving()) return;
    const trimmedName = name().trim();
    const trimmedPrompt = prompt().trim();
    if (!trimmedName || !trimmedPrompt) {
      setError("Name and prompt are required.");
      return;
    }
    const usdNum = Number(spendUsd());
    const spendNano = Math.round(
      (Number.isFinite(usdNum) && usdNum > 0 ? usdNum : 2) * 1_000_000_000,
    );
    const target =
      targetKind() === "existing_chat" && targetSessionId().trim()
        ? { kind: "existing_chat" as const, session_id: targetSessionId().trim() }
        : { kind: "new_chat" as const };
    const gate = buildGate();
    const wf = workflowId().trim();
    const mdl = modelId().trim();
    const tz = timeZone().trim() || "UTC";

    setSaving(true);
    setError(null);
    try {
      let saved: StandingWatch;
      if (props.seed.mode === "keep" && props.seed.keepSource) {
        const res = await keepSessionWatchAction(
          client,
          props.seed.keepSource.sessionId,
          props.seed.keepSource.handle,
          {
            name: trimmedName,
            prompt: trimmedPrompt,
            conditions: conditions(),
            ...(gate ? { gate } : {}),
            min_gap_minutes: minGapMinutes(),
            target,
            ...(wf ? { workflow_id: wf } : {}),
            ...(mdl ? { model_id: mdl } : {}),
            run_spend_limit_nano_usd: spendNano,
            allow_unpriced: allowUnpriced(),
            daily_run_cap: dailyRunCap(),
            time_zone: tz,
            ...(props.seed.revision !== undefined
              ? { revision: props.seed.revision }
              : {}),
          },
        );
        saved = res.standing_watch;
      } else if (props.seed.mode === "edit" && props.seed.watchId) {
        const rev = props.existingWatch?.revision ?? props.seed.revision ?? 1;
        saved = await updateProjectStandingWatchAction(
          client,
          props.projectId,
          props.seed.watchId,
          {
            revision: rev,
            name: trimmedName,
            prompt: trimmedPrompt,
            conditions: conditions(),
            ...(gate ? { gate } : { clear_gate: true }),
            min_gap_minutes: minGapMinutes(),
            target,
            ...(wf ? { workflow_id: wf } : {}),
            ...(mdl ? { model_id: mdl } : {}),
            run_spend_limit_nano_usd: spendNano,
            allow_unpriced: allowUnpriced(),
            daily_run_cap: dailyRunCap(),
            time_zone: tz,
            ...(enableWatch() ? { state: "enabled" } : {}),
          },
        );
      } else {
        saved = await createProjectStandingWatchAction(client, props.projectId, {
          name: trimmedName,
          prompt: trimmedPrompt,
          conditions: conditions(),
          ...(gate ? { gate } : {}),
          min_gap_minutes: minGapMinutes(),
          target,
          ...(wf ? { workflow_id: wf } : {}),
          ...(mdl ? { model_id: mdl } : {}),
          run_spend_limit_nano_usd: spendNano,
          allow_unpriced: allowUnpriced(),
          daily_run_cap: dailyRunCap(),
          time_zone: tz,
        });
      }
      props.onSaved(saved);
    } catch (err) {
      setError(formatHostWatchError(err));
    } finally {
      setSaving(false);
    }
  };

  const titleLabel = () => {
    if (props.seed.mode === "keep") return "Keep as standing watch";
    if (props.seed.mode === "edit") return "Edit standing watch";
    return "New standing watch";
  };

  return (
    <form
      class="flex flex-col gap-den-3 rounded-den-md border border-den-line bg-den-bg-surface p-den-3"
      data-testid="standing-watch-editor"
      onSubmit={(e) => void handleSubmit(e)}
    >
      <div class="flex flex-wrap items-center justify-between gap-den-2">
        <h3 class="text-den-body font-medium text-den-text-primary">
          {titleLabel()}
        </h3>
        <DenButton
          variant="ghost"
          compact
          data-testid="standing-watch-editor-close"
          onClick={() => props.onCancel()}
        >
          Close
        </DenButton>
      </div>

      <label class="flex flex-col gap-den-1 text-den-caption">
        <span>
          Name {props.seed.changedFields.has("name") ? "(changed)" : ""}
        </span>
        <input
          type="text"
          maxLength={80}
          required
          data-testid="standing-watch-name-input"
          class="rounded-den-sm border bg-den-bg-base px-den-2 py-den-1 text-den-body"
          classList={{
            "border-den-accent": props.seed.changedFields.has("name"),
            "border-den-line": !props.seed.changedFields.has("name"),
          }}
          value={name()}
          onInput={(e) => setName(e.currentTarget.value)}
        />
      </label>

      <label class="flex flex-col gap-den-1 text-den-caption">
        <span>
          Prompt {props.seed.changedFields.has("prompt") ? "(changed)" : ""}
        </span>
        <textarea
          rows={3}
          maxLength={8000}
          required
          data-testid="standing-watch-prompt-input"
          class="rounded-den-sm border bg-den-bg-base px-den-2 py-den-1 text-den-body"
          classList={{
            "border-den-accent": props.seed.changedFields.has("prompt"),
            "border-den-line": !props.seed.changedFields.has("prompt"),
          }}
          value={prompt()}
          onInput={(e) => setPrompt(e.currentTarget.value)}
        />
      </label>

      <StandingWatchConditionFields
        conditions={conditions()}
        highlighted={props.seed.changedFields.has("schedule")}
        onChange={setConditions}
      />

      <div class="flex flex-wrap items-start gap-den-3">
        <label class="flex flex-col gap-den-1 text-den-caption">
          <span>Time zone</span>
          <input
            type="text"
            data-testid="standing-watch-timezone-input"
            class="w-56 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 text-den-body"
            value={timeZone()}
            onInput={(e) => setTimeZone(e.currentTarget.value)}
          />
        </label>

        <div
          class="flex flex-col gap-den-1 text-den-caption"
          data-testid="standing-watch-next-runs"
        >
          <span class="font-medium text-den-text-primary">
            Next runs ({zoneValid() ? timeZone().trim() : "invalid time zone"})
          </span>
          <Show
            when={zoneValid() && nextThreeRuns().length > 0}
            fallback={
              <span class="text-den-text-muted">
                {zoneValid()
                  ? "Driven by repository or file changes"
                  : "Enter a valid IANA time zone"}
              </span>
            }
          >
            <ul class="flex flex-wrap gap-den-2 text-den-text-muted">
              <For each={nextThreeRuns()}>
                {(ts) => <li>{formatRunInTimeZone(ts, timeZone())}</li>}
              </For>
            </ul>
          </Show>
        </div>
      </div>

      <div class="flex flex-wrap gap-den-3">
        <div class="flex flex-col gap-den-1 text-den-caption">
          <span>Target</span>
          <DenSelect
            aria-label="Standing watch target"
            data-testid="standing-watch-target-select"
            options={TARGET_KIND_OPTIONS}
            value={targetKind()}
            onValueChange={(v) =>
              setTargetKind(v as "new_chat" | "existing_chat")
            }
          />
        </div>

        <Show when={targetKind() === "existing_chat"}>
          <label class="flex flex-col gap-den-1 text-den-caption">
            <span>Chat session ID</span>
            <input
              type="text"
              data-testid="standing-watch-target-session-input"
              class="w-64 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 font-mono text-den-body"
              value={targetSessionId()}
              onInput={(e) => setTargetSessionId(e.currentTarget.value)}
            />
          </label>
        </Show>

        <label class="flex flex-col gap-den-1 text-den-caption">
          <span>Minimum gap (minutes)</span>
          <DenNumberInput
            min={15}
            max={1440}
            data-testid="standing-watch-mingap-input"
            class="w-28"
            value={minGapMinutes()}
            onNumber={(num) => setMinGapMinutes(num ?? 15)}
          />
        </label>

        <label class="flex flex-col gap-den-1 text-den-caption">
          <span>Run spend limit (USD)</span>
          <DenNumberInput
            step="0.1"
            min="0.1"
            max="100"
            data-testid="standing-watch-spend-input"
            class="w-28"
            value={spendUsd()}
            onInput={(e) => setSpendUsd(e.currentTarget.value)}
          />
        </label>

        <label class="flex flex-col gap-den-1 text-den-caption">
          <span>Daily run cap</span>
          <DenNumberInput
            min={1}
            max={96}
            data-testid="standing-watch-dailycap-input"
            class="w-24"
            value={dailyRunCap()}
            onNumber={(num) => setDailyRunCap(num ?? 1)}
          />
        </label>
      </div>

      <div class="flex flex-wrap items-end gap-den-3">
        <div class="flex flex-col gap-den-1 text-den-caption">
          <span>Gate (optional)</span>
          <DenSelect
            aria-label="Standing watch gate"
            data-testid="standing-watch-gate-select"
            options={GATE_KIND_OPTIONS}
            value={gateKind()}
            onValueChange={(v) =>
              setGateKind(v as "" | "git_ref" | "file_change")
            }
          />
        </div>

        <Show when={gateKind() !== ""}>
          <label class="flex flex-col gap-den-1 text-den-caption">
            <span>{gateKind() === "git_ref" ? "Gate ref" : "Gate path"}</span>
            <input
              type="text"
              data-testid="standing-watch-gate-target-input"
              class="w-48 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 font-mono text-den-body"
              value={gateTarget()}
              onInput={(e) => setGateTarget(e.currentTarget.value)}
            />
          </label>
        </Show>

        <label class="flex flex-col gap-den-1 text-den-caption">
          <span>Workflow</span>
          <input
            type="text"
            data-testid="standing-watch-workflow-input"
            class="w-36 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 text-den-body"
            value={workflowId()}
            onInput={(e) => setWorkflowId(e.currentTarget.value)}
          />
        </label>

        <label class="flex flex-col gap-den-1 text-den-caption">
          <span>Model (optional)</span>
          <input
            type="text"
            data-testid="standing-watch-model-input"
            class="w-44 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 text-den-body"
            value={modelId()}
            onInput={(e) => setModelId(e.currentTarget.value)}
          />
        </label>

        <DenCheckbox
          class="py-den-1 text-den-caption"
          data-testid="standing-watch-unpriced-checkbox"
          checked={allowUnpriced()}
          onChange={(e) => setAllowUnpriced(e.currentTarget.checked)}
        >
          Allow unpriced models
        </DenCheckbox>

        <Show when={props.existingWatch?.state === "disabled"}>
          <DenCheckbox
            class="py-den-1 text-den-caption"
            data-testid="standing-watch-enable-takeover"
            checked={enableWatch()}
            onChange={(e) => setEnableWatch(e.currentTarget.checked)}
          >
            Enable and take ownership
          </DenCheckbox>
        </Show>
      </div>

      <Show when={error()}>
        {(msg) => (
          <p
            role="alert"
            class="text-den-caption text-den-err"
            data-testid="standing-watch-editor-error"
          >
            {msg()}
          </p>
        )}
      </Show>

      <div class="flex flex-wrap items-center gap-den-2 pt-den-1">
        <DenButton
          type="submit"
          variant="primary"
          compact
          disabled={saving() || !zoneValid()}
          data-testid="standing-watch-editor-save"
        >
          {saving()
            ? "Saving…"
            : props.seed.mode === "keep"
              ? "Keep"
              : "Save standing watch"}
        </DenButton>
        <DenButton
          variant="secondary"
          compact
          disabled={saving()}
          data-testid="standing-watch-editor-cancel"
          onClick={() => props.onCancel()}
        >
          Cancel
        </DenButton>
      </div>
    </form>
  );
}
