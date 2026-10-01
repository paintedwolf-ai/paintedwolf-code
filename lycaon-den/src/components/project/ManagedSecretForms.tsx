import { Show, createMemo, createSignal, createUniqueId } from "solid-js";
import {
  MANAGED_SECRETS_COPY as C,
  draftProblemMessage,
  agentUseDeadlineChoiceLabel,
  agentUseDeadlineProblemMessage,
} from "../../settings/security/managed-secrets-copy.ts";
import {
  AGENT_USE_DEADLINE_CHOICES,
  SECRET_NAME_MAX,
  SECRET_PURPOSE_MAX,
  addDraftProblem,
  labelProblem,
  resolveAgentUseDeadline,
  valueProblem,
  type AgentUseDeadlineChoice,
  type SecretDraft,
} from "../../settings/security/managed-secrets-model.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenField } from "../primitives/DenField.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";

function SecretValueField(props: {
  label: string;
  hint: string;
  value: string;
  testId: string;
  error?: string;
  onInput: (value: string) => void;
}) {
  const [revealed, setRevealed] = createSignal(false);
  const inputId = createUniqueId();
  return (
    <DenField
      for={inputId}
      label={
        <span class="den-settings-provider-key-label">
          <span>{props.label}</span>
          <button
            type="button"
            class="den-settings-inline-toggle"
            data-testid={`${props.testId}-reveal`}
            aria-label={revealed() ? "Hide value" : "Show value"}
            aria-pressed={revealed()}
            onClick={() => setRevealed(!revealed())}
          >
            {revealed() ? C.hideValue : C.showValue}
          </button>
        </span>
      }
      hint={props.hint}
      error={props.error}
    >
      <DenInput
        id={inputId}
        aria-label={props.label}
        type={revealed() ? "text" : "password"}
        autocomplete="off"
        spellcheck={false}
        class="den-settings-api-key-input"
        data-testid={props.testId}
        placeholder={C.addValuePlaceholder}
        value={props.value}
        onInput={(event) => props.onInput(event.currentTarget.value)}
      />
    </DenField>
  );
}

function AgentUseDeadlineField(props: {
  choice: AgentUseDeadlineChoice;
  customDate: string;
  problem?: string;
  onChoice: (choice: AgentUseDeadlineChoice) => void;
  onCustomDate: (value: string) => void;
}) {
  return (
    <>
      <DenField label={C.addAgentUseDeadline} hint={C.agentUseDeadlineHint}>
        <DenSelect
          aria-label={C.addAgentUseDeadline}
          data-testid="managed-secret-agent-use-deadline-choice"
          value={props.choice}
          options={AGENT_USE_DEADLINE_CHOICES.map((choice) => ({
            value: choice,
            label: agentUseDeadlineChoiceLabel(choice),
          }))}
          onValueChange={(value) => props.onChoice(value as AgentUseDeadlineChoice)}
        />
      </DenField>
      <Show when={props.choice === "custom"}>
        <DenField label={C.deadlineCustomLabel} error={props.problem}>
          <DenInput
            type="date"
            data-testid="managed-secret-agent-use-deadline-date"
            value={props.customDate}
            onInput={(event) => props.onCustomDate(event.currentTarget.value)}
          />
        </DenField>
      </Show>
    </>
  );
}

type AddFormProps = {
  busy: boolean;
  onCancel: () => void;
  onSubmit: (draft: SecretDraft) => void;
};

export function ManagedSecretAddForm(props: AddFormProps) {
  const [name, setName] = createSignal("");
  const [purpose, setPurpose] = createSignal("");
  const [value, setValue] = createSignal("");
  const [choice, setChoice] = createSignal<AgentUseDeadlineChoice>("never");
  const [customDate, setCustomDate] = createSignal("");
  const [showProblems, setShowProblems] = createSignal(false);

  const draft = (): SecretDraft => ({
    name: name(),
    purpose: purpose(),
    value: value(),
    agentUseEndsAt: "",
  });
  const problem = createMemo(() => addDraftProblem(draft()));
  const deadline = createMemo(() => resolveAgentUseDeadline(choice(), customDate(), new Date()));
  const deadlineProblem = createMemo(() => {
    const resolved = deadline();
    return "problem" in resolved ? agentUseDeadlineProblemMessage(resolved.problem) : undefined;
  });

  const problemFor = (kinds: string[]) => {
    const found = problem();
    if (!showProblems() || !found || !kinds.includes(found)) return undefined;
    return draftProblemMessage(found);
  };

  const submit = () => {
    setShowProblems(true);
    const resolved = deadline();
    if (problem() || "problem" in resolved) return;
    props.onSubmit({ ...draft(), agentUseEndsAt: resolved.at });
  };

  return (
    <article class="den-settings-detail" data-testid="managed-secret-add-form">
      <header class="den-settings-detail__header">
        <div>
          <h3>{C.addHeading}</h3>
          <p>{C.addLede}</p>
        </div>
      </header>

      <DenField label={C.addName} error={problemFor(["name_missing", "name_too_long"])}>
        <DenInput
          data-testid="managed-secret-add-name"
          maxlength={SECRET_NAME_MAX}
          placeholder={C.addNamePlaceholder}
          value={name()}
          onInput={(event) => setName(event.currentTarget.value)}
        />
      </DenField>

      <DenField
        label={C.addPurpose}
        hint={C.addPurposeHint}
        error={problemFor(["purpose_missing", "purpose_too_long"])}
      >
        <DenInput
          data-testid="managed-secret-add-purpose"
          maxlength={SECRET_PURPOSE_MAX}
          placeholder={C.addPurposePlaceholder}
          value={purpose()}
          onInput={(event) => setPurpose(event.currentTarget.value)}
        />
      </DenField>

      <SecretValueField
        label={C.addValue}
        hint={C.addValueHint}
        value={value()}
        testId="managed-secret-add-value"
        error={problemFor(["value_missing", "value_too_short", "value_too_long"])}
        onInput={setValue}
      />

      <AgentUseDeadlineField
        choice={choice()}
        customDate={customDate()}
        problem={showProblems() ? deadlineProblem() : undefined}
        onChoice={setChoice}
        onCustomDate={setCustomDate}
      />

      <div class="den-settings-provider-actions">
        <DenButton
          variant="primary"
          compact
          disabled={props.busy}
          data-testid="managed-secret-add-submit"
          onClick={submit}
        >
          {props.busy ? C.submittingAdd : C.submitAdd}
        </DenButton>
        <DenButton
          variant="secondary"
          compact
          disabled={props.busy}
          data-testid="managed-secret-add-cancel"
          onClick={() => props.onCancel()}
        >
          {C.cancel}
        </DenButton>
      </div>
    </article>
  );
}

type EditFormProps = {
  name: string;
  purpose: string;
  busy: boolean;
  onCancel: () => void;
  onSubmit: (patch: { name: string; purpose: string }) => void;
};

export function ManagedSecretEditForm(props: EditFormProps) {
  const [name, setName] = createSignal(props.name);
  const [purpose, setPurpose] = createSignal(props.purpose);
  const [showProblems, setShowProblems] = createSignal(false);

  // Purpose stays optional when scope is unchanged.
  const problem = createMemo(() => labelProblem(name(), purpose(), false));
  const problemFor = (kinds: string[]) => {
    const found = problem();
    if (!showProblems() || !found || !kinds.includes(found)) return undefined;
    return draftProblemMessage(found);
  };

  const submit = () => {
    setShowProblems(true);
    if (problem()) return;
    props.onSubmit({ name: name().trim(), purpose: purpose().trim() });
  };

  return (
    <section class="den-settings-detail__form" data-testid="managed-secret-edit-form">
      <h4>{C.editHeading}</h4>
      <DenField label={C.addName} error={problemFor(["name_missing", "name_too_long"])}>
        <DenInput
          data-testid="managed-secret-edit-name"
          maxlength={SECRET_NAME_MAX}
          value={name()}
          onInput={(event) => setName(event.currentTarget.value)}
        />
      </DenField>
      <DenField
        label={C.addPurpose}
        error={problemFor(["purpose_missing", "purpose_too_long"])}
      >
        <DenInput
          data-testid="managed-secret-edit-purpose"
          maxlength={SECRET_PURPOSE_MAX}
          value={purpose()}
          onInput={(event) => setPurpose(event.currentTarget.value)}
        />
      </DenField>
      <FormActions
        busy={props.busy}
        submitLabel={C.save}
        busyLabel={C.saving}
        testId="managed-secret-edit"
        onCancel={props.onCancel}
        onSubmit={submit}
      />
    </section>
  );
}

type ValueFormProps = {
  restoring: boolean;
  markedInFile: boolean;
  busy: boolean;
  onCancel: () => void;
  onSubmit: (value: string) => void;
};

export function ManagedSecretValueForm(props: ValueFormProps) {
  const [value, setValue] = createSignal("");
  const [showProblems, setShowProblems] = createSignal(false);
  const problem = createMemo(() => valueProblem(value()));
  const valueError = () => {
    const found = problem();
    return showProblems() && found ? draftProblemMessage(found) : undefined;
  };

  const submit = () => {
    setShowProblems(true);
    if (problem()) return;
    props.onSubmit(value());
  };

  return (
    <section class="den-settings-detail__form" data-testid="managed-secret-value-form">
      <h4>{props.restoring ? C.restoreHeading : C.replaceHeading}</h4>
      <p class="den-settings-hint">
        {props.restoring
          ? C.restoreHint
          : props.markedInFile
            ? C.replaceMarkedHint
            : C.replaceHint}
      </p>
      <SecretValueField
        label={C.newValue}
        hint={C.addValueHint}
        value={value()}
        testId="managed-secret-new-value"
        error={valueError()}
        onInput={setValue}
      />
      <FormActions
        busy={props.busy}
        submitLabel={props.restoring ? C.submitRestore : C.submitReplace}
        busyLabel={C.replacing}
        testId="managed-secret-value"
        onCancel={props.onCancel}
        onSubmit={submit}
      />
    </section>
  );
}

type AgentUseDeadlineFormProps = {
  busy: boolean;
  onCancel: () => void;
  onSubmit: (at: string) => void;
};

export function ManagedSecretAgentUseDeadlineForm(props: AgentUseDeadlineFormProps) {
  const [choice, setChoice] = createSignal<AgentUseDeadlineChoice>("never");
  const [customDate, setCustomDate] = createSignal("");
  const [showProblems, setShowProblems] = createSignal(false);
  const deadline = createMemo(() => resolveAgentUseDeadline(choice(), customDate(), new Date()));

  const submit = () => {
    setShowProblems(true);
    const resolved = deadline();
    if ("problem" in resolved) return;
    props.onSubmit(resolved.at);
  };

  const problem = () => {
    const resolved = deadline();
    if (!showProblems() || !("problem" in resolved)) return undefined;
    return agentUseDeadlineProblemMessage(resolved.problem);
  };

  return (
    <section class="den-settings-detail__form" data-testid="managed-secret-agent-use-deadline-form">
      <h4>{C.agentUseDeadlineHeading}</h4>
      <AgentUseDeadlineField
        choice={choice()}
        customDate={customDate()}
        problem={problem()}
        onChoice={setChoice}
        onCustomDate={setCustomDate}
      />
      <FormActions
        busy={props.busy}
        submitLabel={C.save}
        busyLabel={C.saving}
        testId="managed-secret-agent-use-deadline"
        onCancel={props.onCancel}
        onSubmit={submit}
      />
    </section>
  );
}

function FormActions(props: {
  busy: boolean;
  submitLabel: string;
  busyLabel: string;
  testId: string;
  onCancel: () => void;
  onSubmit: () => void;
}) {
  return (
    <div class="den-settings-provider-actions">
      <DenButton
        variant="primary"
        compact
        disabled={props.busy}
        data-testid={`${props.testId}-submit`}
        onClick={() => props.onSubmit()}
      >
        {props.busy ? props.busyLabel : props.submitLabel}
      </DenButton>
      <DenButton
        variant="secondary"
        compact
        disabled={props.busy}
        data-testid={`${props.testId}-cancel`}
        onClick={() => props.onCancel()}
      >
        {C.cancel}
      </DenButton>
    </div>
  );
}
