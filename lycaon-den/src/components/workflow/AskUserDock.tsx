import { For, Show, createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { PromptSecretReferencePart, WorkflowFeedbackMeta } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import {
  composeFeedbackSubmitAnswer,
  isFeedbackNoLongerPendingError,
} from "../../workflow/workflow-feedback-card-model.ts";
import { MarkdownBody } from "../transcript/MarkdownBody.tsx";
import { FeedbackArtifactThumb } from "./WorkflowFeedbackArtifacts.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { DenRadioControl } from "../primitives/DenRadio.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { hasComposerDraft } from "../../chat/composer/composer-drafts.ts";
import {
  createDockCollapse,
  DockCollapseBody,
  DockCollapseHead,
} from "../primitives/DockCollapse.tsx";

export type AskUserDockSubmitFn = (
  composerText: string,
  secrets?: PromptSecretReferencePart[],
) => Promise<boolean>;

type Props = {
  meta: WorkflowFeedbackMeta;
  client: LycaonClient;
  sessionId: string;
  runId: string;
  runRevision: number;
  appStore?: AppStore | null;
  entryKey?: string;
  registerSubmit?: (fn: AskUserDockSubmitFn | undefined) => void;
  onAnswerReadyChange?: (ready: boolean) => void;
  minimized?: boolean;
  onMinimizedChange?: (next: boolean) => void;
  onResolved?: () => void;
};

const COMPARE_LABELS = ["A", "B", "C", "D"] as const;

const PROMPT_MAX_HEIGHT = "12rem";

function agentUseLifetime(ttlMs: number | undefined): string {
  if (!ttlMs || ttlMs <= 0) return "No agent-use deadline";
  const seconds = Math.round(ttlMs / 1000);
  if (seconds % 86_400 === 0) {
    const days = seconds / 86_400;
    return `Agent use ends in ${days} day${days === 1 ? "" : "s"}`;
  }
  if (seconds % 3_600 === 0) {
    const hours = seconds / 3_600;
    return `Agent use ends in ${hours} hour${hours === 1 ? "" : "s"}`;
  }
  if (seconds % 60 === 0) {
    const minutes = seconds / 60;
    return `Agent use ends in ${minutes} minute${minutes === 1 ? "" : "s"}`;
  }
  return `Agent use ends in ${seconds} seconds`;
}

export function AskUserDock(props: Props) {
  const collapse = createDockCollapse({
    minimized: () => props.minimized,
    onMinimizedChange: (next) => props.onMinimizedChange?.(next),
  });
  const responseType = () => props.meta.response_type ?? "text";
  const options = () => props.meta.options ?? [];
  const artifactId = () => (props.meta.artifact_id ?? "").trim();
  const compareIds = createMemo(() => {
    const ids = (props.meta.artifact_ids ?? [])
      .map((id) => id.trim())
      .filter(Boolean)
      .slice(0, 4);
    if (ids.length >= 2) return ids;
    const one = artifactId();
    return one ? [one] : [];
  });
  const isCompare = () =>
    props.meta.purpose === "compare" && (props.meta.artifact_ids?.length ?? 0) >= 2;
  const isMulti = () => responseType() === "multi_choice";
  const isText = () => responseType() === "text";
  const isSecret = () => responseType() === "secret";
  const compareOption = (index: number) =>
    options()[index] ?? COMPARE_LABELS[index] ?? String(index + 1);

  const [singleChoice, setSingleChoice] = createSignal<string | null>(null);
  const [compareChoice, setCompareChoice] = createSignal<string | null>(null);
  const [multi, setMulti] = createSignal<Record<string, boolean>>({});
  const [submitting, setSubmitting] = createSignal(false);
  const [error, setError] = createSignal<string | null>(null);
  const [secretValue, setSecretValue] = createSignal("");
  const [secretVisible, setSecretVisible] = createSignal(false);

  const promptPeek = createMemo(() =>
    props.meta.prompt.replace(/\s+/g, " ").trim(),
  );

  const toggleMulti = (key: string) =>
    setMulti((m) => ({ ...m, [key]: !m[key] }));

  const composedChoices = (composerText: string): string[] => {
    const other = composerText.trim();
    const picked = options().filter((o) => multi()[o]);
    if (other) picked.push(other);
    return picked;
  };

  // Returns the row to clear if submission fails.
  const stampOptimisticAnswer = (
    answer: string,
    resolvedBy: string,
  ): string | undefined => {
    const store = props.appStore;
    const id = (props.entryKey ?? "").trim();
    if (!store || !id) return undefined;
    const existing = store.state.messages.find((m) => m.id === id);
    if (!existing?.workflow_feedback) return undefined;
    store.actions.upsertMessage({
      ...existing,
      workflow_feedback: {
        ...existing.workflow_feedback,
        answer,
        resolved_by: resolvedBy,
      },
    });
    return id;
  };

  const clearOptimisticAnswer = (id: string | undefined) => {
    const store = props.appStore;
    if (!store || !id) return;
    const existing = store.state.messages.find((m) => m.id === id);
    if (!existing?.workflow_feedback) return;
    store.actions.upsertMessage({
      ...existing,
      workflow_feedback: {
        ...existing.workflow_feedback,
        answer: undefined,
        resolved_by: undefined,
      },
    });
  };

  const resolveWith = async (
    rt: string,
    submittedText: string,
    submittedChoices: string[],
    secrets?: PromptSecretReferencePart[],
  ) => {
    if (submitting()) return false;
    const phaseId = props.meta.phase_id;
    if (!phaseId) return false;
    if (!props.runId || props.runRevision < 1) {
      setError("This workflow changed. Refresh it before answering.");
      return false;
    }
    if (rt === "text") {
      if (!submittedText.trim() && !secrets?.length) return false;
    } else if (submittedChoices.length === 0) {
      return false;
    }
    const optimistic = composeFeedbackSubmitAnswer(rt, submittedText, submittedChoices);
    const stampedId = stampOptimisticAnswer(optimistic, "user");
    setSubmitting(true);
    setError(null);
    const submitAtRevision = (revision: number) => {
      if (rt === "text") {
        return props.client.resolveWorkflowFeedback(props.runId, phaseId, {
          expected_revision: revision,
          response: submittedText.trim(),
          secrets,
        });
      }
      return props.client.resolveWorkflowDecision(props.runId, phaseId, {
        expected_revision: revision,
        choices: submittedChoices,
        secrets,
      });
    };
    try {
      await submitAtRevision(props.runRevision);
      props.onResolved?.();
      return true;
    } catch (err) {
      if (isFeedbackNoLongerPendingError(err)) {
        clearOptimisticAnswer(stampedId);
        setError(null);
        props.onResolved?.();
        return true;
      }
      clearOptimisticAnswer(stampedId);
      setError(err instanceof Error ? err.message : String(err));
      return false;
    } finally {
      setSubmitting(false);
    }
  };

  const resolveSecret = async () => {
    if (submitting() || secretValue().length === 0) return false;
    const phaseId = props.meta.phase_id;
    if (!phaseId || !props.runId || props.runRevision < 1) {
      setError("This workflow changed. Refresh it before answering.");
      return false;
    }
    setSubmitting(true);
    setError(null);
    try {
      await props.client.resolveWorkflowSecret(props.runId, phaseId, {
        expected_revision: props.runRevision,
        secret_value: secretValue(),
      });
      setSecretValue("");
      setSecretVisible(false);
      props.onResolved?.();
      return true;
    } catch (err) {
      if (isFeedbackNoLongerPendingError(err)) {
        setSecretValue("");
        setSecretVisible(false);
        props.onResolved?.();
        return true;
      }
      setError(err instanceof Error ? err.message : String(err));
      return false;
    } finally {
      setSubmitting(false);
    }
  };

  const submitFromComposer: AskUserDockSubmitFn = async (composerText, secrets) => {
    const ok = await submitAnswer(composerText, secrets);
    // Reveal incomplete answers.
    if (!ok && collapse.minimized()) collapse.setMinimized(false);
    return ok;
  };

  const submitAnswer: AskUserDockSubmitFn = async (composerText, secrets) => {
    const rt = responseType();
    if (isSecret()) return resolveSecret();
    // Multi-choice includes typed text as an additional choice.
    if (isMulti()) {
      return resolveWith(rt, composerText, composedChoices(composerText), secrets);
    }
    if (rt === "text" || (!isCompare() && options().length === 0)) {
      return resolveWith("text", composerText, [], secrets);
    }
    // Composer text overrides a selected option.
    const other = composerText.trim();
    if (other) return resolveWith("single_choice", "", [other], secrets);
    const picked = isCompare() ? compareChoice() : singleChoice();
    return resolveWith("single_choice", "", picked ? [picked] : [], secrets);
  };

  createEffect(() => {
    const register = props.registerSubmit;
    if (!register) return;
    register(submitFromComposer);
    onCleanup(() => register(undefined));
  });

  /** Selection-only submission state. */
  const answerReady = createMemo(() => {
    if (isSecret()) return secretValue().length > 0;
    if (isCompare()) return Boolean(compareChoice());
    if (isText() || options().length === 0) return false;
    if (isMulti()) return options().some((opt) => multi()[opt]);
    return Boolean(singleChoice());
  });

  createEffect(() => props.onAnswerReadyChange?.(answerReady()));
  onCleanup(() => props.onAnswerReadyChange?.(false));

  const hint = createMemo(() => {
    if (isSecret()) return "The agent receives only a managed reference, never this value.";
    if (isText()) return undefined;
    const send = bindingForHandler("composer.send");
    if (answerReady()) return `Press ${send} to submit your answer`;
    if (isCompare()) return `Click an image to select it, then ${send}`;
    if (isMulti()) return `Select one or more, then ${send}`;
    return `Select one, then ${send}`;
  });

  const composerArmed = createMemo(() => hasComposerDraft(props.sessionId));

  const showsRail = () => !isText() && !isSecret() && (isCompare() || options().length > 0);

  const railText = createMemo(() => {
    if (!composerArmed()) return undefined;
    return isMulti()
      ? "Send adds what you typed to the options you picked."
      : "Send submits what you typed instead of an option.";
  });

  return (
    <div
      class="den-ask-user-dock den-dock-collapse"
      data-testid="ask-user-dock"
      data-response-type={responseType()}
      data-purpose={props.meta.purpose || undefined}
      data-phase-id={props.meta.phase_id}
      data-minimized={collapse.minimized() ? "" : undefined}
      data-composer-armed={composerArmed() ? "" : undefined}
      role="region"
      aria-label="Your input"
    >
      <DockCollapseHead
        collapse={collapse}
        class="den-ask-user-dock-head"
        lead={() => (
          <span class="den-ask-user-dock-eyebrow">
            <span class="den-ask-user-dock-eyebrow-dot" aria-hidden="true" />
            Your input
          </span>
        )}
        peek={promptPeek()}
        expandLabel="Expand your input"
        minimizeLabel="Minimize your input"
        expandTestid="ask-user-dock-expand"
        minimizeTestid="ask-user-dock-minimize"
      />

      <DockCollapseBody>
        <Scrollport
          class="den-ask-user-dock-prompt"
          data-testid="workflow-feedback-prompt"
          style={{ "max-height": PROMPT_MAX_HEIGHT }}
        >
          <MarkdownBody source={props.meta.prompt} />
        </Scrollport>

        {(() => {
          const ids = compareIds();
          if (ids.length === 0) return null;
          return (
              <div
                class="den-workflow-feedback-card-compare-grid"
                classList={{
                  "den-workflow-feedback-card-compare-grid--pair": ids.length === 2,
                  "den-workflow-feedback-card-compare-grid--trio": ids.length === 3,
                  "den-workflow-feedback-card-compare-grid--quad": ids.length >= 4,
                  "den-ask-user-dock-superseded": composerArmed(),
                }}
                data-testid="workflow-feedback-compare-grid"
              >
                <For each={ids}>
                  {(id, index) => (
                    <FeedbackArtifactThumb
                      client={props.client}
                      sessionId={props.sessionId}
                      artifactId={id}
                      selectable={isCompare()}
                      selected={compareChoice() === compareOption(index())}
                      disabled={submitting()}
                      label={isCompare() ? compareOption(index()) : undefined}
                      radioName={`ask-user-dock-artifact-${props.meta.phase_id}`}
                      onSelect={() => setCompareChoice(compareOption(index()))}
                    />
                  )}
                </For>
              </div>
          );
        })()}

        <Show when={!isText() && !isMulti() && !isCompare()}>
          <Scrollport
            class="den-ask-user-dock-options"
            classList={{ "den-ask-user-dock-superseded": composerArmed() }}
            contentClass="den-ask-user-dock-options__content"
            role="radiogroup"
            aria-label="Choices"
          >
            <For each={options()}>
              {(opt) => (
                <label class="den-ask-user-dock-opt" data-testid="workflow-feedback-option">
                  <DenRadioControl
                    name={`ask-user-dock-choice-${props.meta.phase_id}`}
                    value={opt}
                    checked={singleChoice() === opt}
                    disabled={submitting()}
                    onChange={() => setSingleChoice(opt)}
                  />
                  <span class="den-ask-user-dock-opt-label">{opt}</span>
                </label>
              )}
            </For>
          </Scrollport>
        </Show>

        <Show when={isMulti()}>
          <Scrollport
            class="den-ask-user-dock-options"
            contentClass="den-ask-user-dock-options__content"
            role="group"
            aria-label="Choices"
          >
            <For each={options()}>
              {(opt) => (
                <label class="den-ask-user-dock-opt" data-testid="workflow-feedback-option">
                  <DenCheckboxControl
                    value={opt}
                    checked={Boolean(multi()[opt])}
                    disabled={submitting()}
                    onChange={() => toggleMulti(opt)}
                  />
                  <span class="den-ask-user-dock-opt-label">{opt}</span>
                </label>
              )}
            </For>
          </Scrollport>
        </Show>

        <Show when={isSecret()}>
          <div class="den-ask-user-dock-secret" data-testid="workflow-secret-input">
            <label class="den-ask-user-dock-secret-label" for={`workflow-secret-${props.meta.phase_id}`}>
              {props.meta.secret?.name ?? "Secret value"}
            </label>
            <div class="den-ask-user-dock-secret-row">
              <input
                id={`workflow-secret-${props.meta.phase_id}`}
                class="den-ask-user-dock-secret-input"
                type={secretVisible() ? "text" : "password"}
                autocomplete="new-password"
                autocapitalize="none"
                spellcheck={false}
                value={secretValue()}
                disabled={submitting()}
                onInput={(event) => setSecretValue(event.currentTarget.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && answerReady()) {
                    event.preventDefault();
                    void resolveSecret();
                  }
                }}
              />
              <button
                type="button"
                class="den-ask-user-dock-secret-visibility"
                aria-label={secretVisible() ? "Hide secret" : "Show secret"}
                aria-pressed={secretVisible()}
                disabled={submitting()}
                onClick={() => setSecretVisible((visible) => !visible)}
              >
                {secretVisible() ? "Hide" : "Show"}
              </button>
              <button
                type="button"
                class="den-ask-user-dock-secret-submit"
                disabled={!answerReady() || submitting()}
                onClick={() => void resolveSecret()}
              >
                {submitting() ? "Storing…" : "Store secret"}
              </button>
            </div>
            <Show when={props.meta.secret?.purpose}>
              {(purpose) => <p class="den-ask-user-dock-secret-purpose">{purpose()}</p>}
            </Show>
            <p class="den-ask-user-dock-secret-purpose" data-testid="workflow-secret-policy">
              {props.meta.secret?.scope === "project"
                ? "Available to later chats in this project"
                : "Available only to this chat"}
              {" · "}
              {agentUseLifetime(props.meta.secret?.agent_use_ttl_ms)}
            </p>
          </div>
        </Show>

        <Show when={composerArmed() ? undefined : hint()}>
          {(text) => <p class="den-ask-user-dock-hint">{text()}</p>}
        </Show>
      </DockCollapseBody>

      <Show when={showsRail()}>
        <button
          type="button"
          classList={{
            "den-ask-user-dock-rail": true,
            "den-ask-user-dock-rail--armed": composerArmed(),
          }}
          data-testid="ask-user-redirect-rail"
          onClick={() => focusRegion("composer")}
        >
          <span class="den-ask-user-dock-rail-icon" aria-hidden="true">
            ⤷
          </span>
          <Show
            when={railText()}
            fallback={
              <span>
                Not listed?{" "}
                <span class="den-ask-user-dock-rail-link">type below</span> and
                Send.
              </span>
            }
          >
            {(text) => <span>{text()}</span>}
          </Show>
        </button>
      </Show>

      <Show when={error()}>
        {(msg) => (
          <p class="den-ask-user-dock-error" data-testid="workflow-feedback-error">
            {msg()}
          </p>
        )}
      </Show>
    </div>
  );
}
