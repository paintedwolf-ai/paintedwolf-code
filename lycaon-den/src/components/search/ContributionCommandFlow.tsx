import { For, Show, createEffect, createMemo, createSignal, createUniqueId, onCleanup, onMount } from "solid-js";
import type {
  ContributionChoice,
  ContributionCommand,
  ContributionInputField,
  ContributionInteractionStep,
} from "../../api/types.ts";
import {
  dispatchContributionCommand,
  commandInvokeContext,
  dispatchFailureMessage,
  type DispatchDeps,
} from "../../contributions/dispatch.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenNumberInput } from "../primitives/DenNumberInput.tsx";
import { DenRadio } from "../primitives/DenRadio.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { copyTextToClipboard } from "../../utils/clipboard.ts";

type Props = {
  command: ContributionCommand;
  deps: DispatchDeps;
  onBack: () => void;
  onClose: () => void;
  initialAnswers?: Record<string, unknown>;
  autoSubmit?: boolean;
};

export function ContributionCommandFlow(props: Props) {
  // A form keeps the editor target the person chose when opening it.
  const invocationContext = commandInvokeContext(props.deps.projectId) ?? {};
  const [answers, setAnswers] = createSignal<Record<string, unknown>>(props.initialAnswers ?? {});
  const [stepIndex, setStepIndex] = createSignal(0);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  const [output, setOutput] = createSignal<string | null>(null);
  const [choices, setChoices] = createSignal<Record<string, ContributionChoice[]>>({});
  let dynamicController: AbortController | null = null;
  let flowElement: HTMLDivElement | undefined;
  // Ignore in-flight completions after the flow unmounts.
  let disposed = false;
  onCleanup(() => {
    disposed = true;
  });

  const steps = createMemo(() =>
    (props.command.interaction?.steps ?? []).filter((step) => predicateHolds(step, answers())),
  );
  const currentStep = createMemo(() => steps()[stepIndex()]);

  const update = (id: string, value: unknown) => {
    setAnswers((current) => {
      const next = { ...current, [id]: value };
      for (const step of props.command.interaction?.steps ?? []) {
        if (!predicateHolds(step, next)) delete next[step.id];
      }
      return next;
    });
    setChoices((current) => {
      const next = { ...current };
      for (const step of props.command.interaction?.steps ?? []) {
        if (step.source && Object.values(step.source.inputs ?? {}).includes(id)) {
          delete next[step.id];
          dynamicController?.abort();
          dynamicController = null;
          setBusy(false);
        }
      }
      return next;
    });
    setError("");
  };

  const invoke = async () => {
    setBusy(true);
    setError("");
    const result = await dispatchContributionCommand(props.command.id, props.deps, invocationContext, answers());
    if (disposed) return;
    setBusy(false);
    if (!result.ok) {
      setError(dispatchFailureMessage(result) ?? "The command could not be run.");
      return;
    }
    if (props.command.result_treatment === "output" && result.output != null) {
      setOutput(result.output);
      return;
    }
    props.onClose();
  };

  const submitForm = async (event: SubmitEvent) => {
    event.preventDefault();
    const invalid = validateFields(props.command.input ?? [], answers());
    if (invalid) { setError(invalid); return; }
    await invoke();
  };

  const advanceInteraction = async () => {
    const step = currentStep();
    if (!step) { await invoke(); return; }
    const invalid = validateStep(step, answers()[step.id]);
    if (invalid) { setError(invalid); return; }
    if (stepIndex() + 1 < steps().length) {
      setStepIndex((index) => index + 1);
      setError("");
      return;
    }
    await invoke();
  };

  const loadChoices = async (step: ContributionInteractionStep) => {
    const client = props.deps.client;
    const projectId = props.deps.projectId;
    const frame = contributionFrame();
    if (!client || !projectId || !frame) { setError("This choice source needs an open project."); return; }
    dynamicController?.abort();
    const controller = new AbortController();
    dynamicController = controller;
    const inputKey = choiceInputKey(step, answers());
    setBusy(true); setError("");
    try {
      const response = await client.resolveContributionChoices(
        projectId, props.command.id, step.id,
        { frame_revision: frame.frame_revision, answers: answers() },
        controller.signal,
      );
      if (dynamicController === controller && inputKey === choiceInputKey(step, answers())) {
        setChoices((current) => ({ ...current, [step.id]: response.choices }));
      }
    } catch (cause) {
      if (dynamicController === controller && !controller.signal.aborted) {
        setError(cause instanceof Error ? cause.message : String(cause));
      }
    } finally {
      if (dynamicController === controller) {
        dynamicController = null;
        setBusy(false);
      }
    }
  };

  onMount(() => {
    for (const field of props.command.input ?? []) {
      if (field.default !== undefined && answers()[field.id] === undefined) update(field.id, field.default);
    }
    if (props.autoSubmit || !(props.command.input?.length || props.command.interaction)) void invoke();
    queueMicrotask(() => flowElement?.querySelector<HTMLElement>(
      ".den-command-flow__body input, .den-command-flow__body select, .den-command-flow__body textarea, .den-command-flow__body button",
    )?.focus());
  });
  createEffect(() => {
    const count = steps().length;
    if (count > 0 && stepIndex() >= count) setStepIndex(count - 1);
  });
  createEffect(() => {
    stepIndex();
    output();
    queueMicrotask(() => flowElement?.querySelector<HTMLElement>(
      ".den-command-flow__result pre, .den-command-flow__body input, .den-command-flow__body select, .den-command-flow__body textarea, .den-command-flow__body button",
    )?.focus());
  });
  onCleanup(() => dynamicController?.abort());

  return (
    <div class="den-command-flow" data-testid="contribution-command-flow" ref={(element) => (flowElement = element)}>
      <header class="den-command-flow__header">
        <button type="button" class="den-command-flow__icon-button den-inset-icon-btn" aria-label="Back to results" onClick={props.onBack}>
          <ThemeIcon slot="back" size={14} />
        </button>
        <div>
          <div class="den-command-flow__title">{props.command.title}</div>
          <div class="den-command-flow__provider">{props.command.category || "Other"} · {props.command.provider}</div>
        </div>
        <button type="button" class="den-command-flow__icon-button den-command-flow__close den-inset-icon-btn" aria-label="Close" onClick={props.onClose}>
          <ThemeIcon slot="dismiss" size={14} />
        </button>
      </header>

      <Show when={output() !== null} fallback={
        <Show when={props.command.interaction} fallback={
          <Scrollport
            class="den-command-flow__body"
            contentAs="form"
            contentClass="den-command-flow__content"
            content={{ onSubmit: (event) => void submitForm(event) }}
          >
            <For each={props.command.input ?? []}>{(field) =>
              <InputField field={field} value={answers()[field.id]} onValue={(value) => update(field.id, value)} />
            }</For>
            <Show when={error()}><div class="den-command-flow__error" role="alert">{error()}</div></Show>
            <button class="den-command-flow__primary" type="submit" disabled={busy()}>{busy() ? "Running…" : "Run"}</button>
          </Scrollport>
        }>
          <Scrollport class="den-command-flow__body" contentClass="den-command-flow__content">
            <Show when={currentStep()} keyed fallback={<div role="status">{busy() ? "Running…" : "Preparing…"}</div>}>
              {(step) => <InteractionStepView
                step={step}
                value={answers()[step.id]}
                choices={step.values ?? choices()[step.id] ?? []}
                busy={busy()}
                onValue={(value) => update(step.id, value)}
                onLoad={() => void loadChoices(step)}
              />}
            </Show>
            <Show when={error()}><div class="den-command-flow__error" role="alert">{error()}</div></Show>
            <div class="den-command-flow__actions">
              <button type="button" onClick={() => stepIndex() > 0 ? setStepIndex((index) => index - 1) : props.onBack()}>Back</button>
              <button type="button" class="den-command-flow__primary" disabled={busy()} onClick={() => void advanceInteraction()}>
                {stepIndex() + 1 < steps().length ? "Next" : busy() ? "Running…" : "Run"}
              </button>
            </div>
          </Scrollport>
        </Show>
      }>
        <Scrollport class="den-command-flow__body den-command-flow__result" contentClass="den-command-flow__content">
          <pre data-testid="contribution-command-output" tabindex="-1" aria-label="Command output">{output()}</pre>
          <div class="den-command-flow__actions">
            <button type="button" onClick={props.onBack}>Back</button>
            <button type="button" onClick={() => void copyTextToClipboard(output() ?? "")}>Copy</button>
            <button type="button" class="den-command-flow__primary" onClick={props.onClose}>Close</button>
          </div>
        </Scrollport>
      </Show>
    </div>
  );
}

function InputField(props: { field: ContributionInputField; value: unknown; onValue: (value: unknown) => void; labelledBy?: string }) {
  const field = () => props.field;
  const title = () => `${field().title}${field().required ? " *" : ""}`;
  return <Show when={field().type === "boolean"} fallback={
    <label class="den-command-flow__field">
      <span>{title()}</span>
      <Show when={field().description}><small>{field().description}</small></Show>
      <Show when={field().type === "enum"} fallback={
        <Show when={field().type === "string_list"} fallback={
          <Show when={field().type === "number"} fallback={
            <input
              type="text"
              aria-labelledby={props.labelledBy}
              value={String(props.value ?? "")}
              onInput={(event) => props.onValue(event.currentTarget.value)}
            />
          }>
            <DenNumberInput
              aria-labelledby={props.labelledBy}
              value={String(props.value ?? "")}
              min={field().min}
              max={field().max}
              onInput={(event) => {
                // Clearing a number field removes its answer.
                const parsed = event.currentTarget.valueAsNumber;
                props.onValue(Number.isNaN(parsed) ? undefined : parsed);
              }}
            />
          </Show>
        }>
          <StringListField value={props.value} onValue={props.onValue} labelledBy={props.labelledBy} />
        </Show>
      }>
        <DenSelect
          aria-label={field().title ?? "Choice"}
          value={String(props.value ?? "")}
          placeholder="Select…"
          options={(field().values ?? []).map((value) => ({ value, label: value }))}
          onValueChange={props.onValue}
        />
      </Show>
    </label>
  }>
    <DenCheckbox
      aria-labelledby={props.labelledBy}
      class="den-command-flow__field"
      checked={props.value === true}
      onChange={(event) => props.onValue(event.currentTarget.checked)}
    >
      {title()}
      <Show when={field().description}><small>{field().description}</small></Show>
    </DenCheckbox>
  </Show>;
}

/** Local text preserves trailing newlines while typing. */
function StringListField(props: { value: unknown; onValue: (value: unknown) => void; labelledBy?: string }) {
  const parseList = (text: string) =>
    text.split("\n").map((entry) => entry.trim()).filter(Boolean);
  const external = () => (Array.isArray(props.value) ? (props.value as string[]) : []);
  const [raw, setRaw] = createSignal(external().join("\n"));
  let emittedSignature = JSON.stringify(external());
  createEffect(() => {
    // Equivalent lists preserve the text being edited.
    const incoming = external();
    const signature = JSON.stringify(incoming);
    if (signature === emittedSignature) return;
    emittedSignature = signature;
    setRaw(incoming.join("\n"));
  });
  return (
    <textarea
      aria-labelledby={props.labelledBy}
      value={raw()}
      onInput={(event) => {
        const text = event.currentTarget.value;
        const parsed = parseList(text);
        setRaw(text);
        emittedSignature = JSON.stringify(parsed);
        props.onValue(parsed);
      }}
    />
  );
}

function InteractionStepView(props: { step: ContributionInteractionStep; value: unknown; choices: ContributionChoice[]; busy: boolean; onValue: (value: unknown) => void; onLoad: () => void }) {
  const titleId = createUniqueId();
  return <div class="den-command-flow__step">
    <h3 id={titleId}>{props.step.title}</h3>
    <Show when={props.step.description}><p>{props.step.description}</p></Show>
    <Show when={props.step.kind === "choice"} fallback={
      <InputField field={{ id: props.step.id, title: "", type: stepInputType(props.step), required: props.step.required, min: props.step.min, max: props.step.max }} value={props.value} onValue={props.onValue} labelledBy={titleId} />
    }>
      <Show when={!props.step.source || props.choices.length > 0} fallback={<button type="button" disabled={props.busy} onClick={props.onLoad}>{props.busy ? "Loading…" : "Load choices"}</button>}>
        <div class="den-command-flow__choices" role={props.step.multiple ? "group" : "radiogroup"} aria-labelledby={titleId}>
          <For each={props.choices}>{(choice) => {
            const content = () => <span><strong>{choice.label}</strong><Show when={choice.description}><small>{choice.description}</small></Show><Show when={choice.detail}><small>{choice.detail}</small></Show></span>;
            const checked = () => choiceSelected(props.value, choice.id);
            return <Show when={props.step.multiple} fallback={
              <DenRadio
                name={props.step.id}
                checked={checked()}
                onChange={(event) => props.onValue(nextChoiceValue(props.value, choice.id, false, event.currentTarget.checked))}
              >
                {content()}
              </DenRadio>
            }>
              <DenCheckbox
                checked={checked()}
                onChange={(event) => props.onValue(nextChoiceValue(props.value, choice.id, true, event.currentTarget.checked))}
              >
                {content()}
              </DenCheckbox>
            </Show>;
          }}</For>
        </div>
      </Show>
    </Show>
  </div>;
}

function stepInputType(step: ContributionInteractionStep): ContributionInputField["type"] {
  if (step.kind === "confirmation" || step.kind === "boolean") return "boolean";
  if (step.kind === "project_path") return "project_path";
  if (step.kind === "number") return "number";
  return "string";
}

function predicateHolds(step: ContributionInteractionStep, answers: Record<string, unknown>): boolean {
  return !step.if || Object.is(answers[step.if.step], step.if.is);
}

function choiceInputKey(step: ContributionInteractionStep, answers: Record<string, unknown>): string {
  return JSON.stringify(Object.entries(step.source?.inputs ?? {})
    .sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0)
    .map(([argument, answerId]) => [argument, answers[answerId]]));
}

function validateFields(fields: readonly ContributionInputField[], answers: Record<string, unknown>): string {
  for (const field of fields) {
    if (field.required && emptyValue(answers[field.id])) return `${field.title} is required.`;
  }
  return "";
}

function validateStep(step: ContributionInteractionStep, value: unknown): string {
  if (step.kind === "confirmation" && step.required && value !== true) return `${step.title} must be confirmed.`;
  return step.required && emptyValue(value) ? `${step.title} is required.` : "";
}

function emptyValue(value: unknown): boolean {
  return (
    value == null ||
    value === "" ||
    (typeof value === "number" && Number.isNaN(value)) ||
    (Array.isArray(value) && value.length === 0)
  );
}
function choiceSelected(value: unknown, id: string): boolean { return Array.isArray(value) ? value.includes(id) : value === id; }
function nextChoiceValue(value: unknown, id: string, multiple: boolean, checked: boolean): unknown {
  if (!multiple) return id;
  const current = Array.isArray(value) ? value.filter((member): member is string => typeof member === "string") : [];
  return checked ? [...new Set([...current, id])] : current.filter((member) => member !== id);
}
