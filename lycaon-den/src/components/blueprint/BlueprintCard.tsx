import { For, Show, createEffect, createSignal } from "solid-js";
import {
  blueprintIsTitleOnly,
  blueprintPreviewSource,
} from "../../blueprint/blueprint-workspace-modal.ts";
import {
  BLUEPRINT_CARD_COPY,
  blueprintCardDecision,
  type BlueprintChoiceTransition,
  type BlueprintInlineCardView,
} from "../../blueprint/blueprint-inline-card-model.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { DecisionRow } from "../transcript/DecisionRow.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { MarkdownBody } from "../transcript/MarkdownBody.tsx";
import { focusWithoutScroll } from "../../platform/interaction/focus.ts";

type Props = {
  anchorId: string;
  sessionId?: string | null;
  entryKey?: string;
  view: BlueprintInlineCardView;
  blueprintName: string | null;
  content: string | null;
  loading: boolean;
  warning: string | null;
  choiceTransitions?: readonly BlueprintChoiceTransition[];
  onRevise: (text: string) => void;
  /** Opens the full-screen blueprint workspace. */
  onOpen: () => void;
  onApprove?: () => void;
  onReject?: () => void;
  onChoiceTransition?: (transitionId: string) => void;
  projectId?: string;
};

/** Blueprint decisions remain accessible from the transcript. */
export function BlueprintCard(props: Props) {
  const decision = () => blueprintCardDecision(props.view.status);
  const [expanded, setExpanded] = createSignal(!props.view.collapsed);
  const [revising, setRevising] = createSignal(false);
  const [reviseText, setReviseText] = createSignal("");
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId ?? undefined,
    entryKey: props.entryKey ?? props.anchorId,
  }));

  createEffect(() => {
    props.view.revisionKey;
    if (!props.view.collapsed) setExpanded(true);
    setRevising(false);
    setReviseText("");
  });

  const submitRevision = () => {
    const text = reviseText().trim();
    if (!text) return;
    props.onRevise(text);
    setReviseText("");
    setRevising(false);
  };

  const toggleExpanded = () => {
    setExpanded((open) => !open);
  };

  // Title-only blueprints have no expandable body.
  const hasBody = () =>
    props.loading || !blueprintIsTitleOnly(props.content ?? "");

  return (
    <article
      ref={bindTranscriptEntry}
      class="den-blueprint-inline-card"
      classList={{
        "den-blueprint-inline-card--collapsed": props.view.collapsed,
        "den-blueprint-inline-card--ready": props.view.phase === "ready",
        "den-blueprint-inline-card--changed": props.view.phase === "changed",
        "den-blueprint-inline-card--decided": decision() != null,
      }}
      id={props.anchorId}
      data-testid="blueprint-card"
      data-blueprint-phase={props.view.phase}
      data-blueprint-revision={props.view.revisionKey}
      data-disclosure-key={transcriptDisclosureKey.blueprint(props.entryKey ?? props.anchorId)}
    >
      <header class="den-blueprint-inline-card__head">
        <div class="den-blueprint-inline-card__title-row">
          <h3 class="den-blueprint-inline-card__title">{props.blueprintName ?? "Blueprint"}</h3>
        </div>
        <Show when={hasBody()}>
          <button
            type="button"
            class="den-blueprint-inline-card__toggle"
            data-testid="blueprint-card-toggle"
            onClick={() => toggleExpanded()}
          >
            {expanded() ? BLUEPRINT_CARD_COPY.hide : BLUEPRINT_CARD_COPY.show}
          </button>
        </Show>
      </header>

      <Show when={hasBody() && expanded()}>
        <Scrollport
          class="den-blueprint-inline-card__body"
          data-testid="blueprint-card-body"
        >
          <Show
            when={props.loading}
            fallback={
              <MarkdownBody
                source={blueprintPreviewSource(props.content ?? "")}
                projectId={props.projectId}
              />
            }
          >
            <p class="den-blueprint-inline-card__loading">Loading blueprint…</p>
          </Show>
        </Scrollport>
      </Show>

      <Show when={props.warning}>
        {(text) => (
          <p class="den-blueprint-inline-card__warn" role="status" data-testid="blueprint-card-warning">
            {text()}
          </p>
        )}
      </Show>

      <Show
        when={props.view.showActions}
        fallback={
          <Show when={decision()}>
            <div class="den-blueprint-inline-card__actions" data-testid="blueprint-card-actions">
              <span class="den-blueprint-inline-card__phase" data-testid="blueprint-card-phase">
                {props.view.phaseLabel}
              </span>
              <Show when={decision()} keyed>
                {(record) => (
                  <p
                    class="den-blueprint-inline-card__decision"
                    role="status"
                    data-decision={record.kind}
                    data-testid="blueprint-card-decision"
                  >
                    <DecisionRow
                      tone={record.kind}
                      label={record.label}
                      details={[
                        { text: props.blueprintName ?? "Blueprint", truncate: true },
                      ]}
                    />
                  </p>
                )}
              </Show>
              <DenButton
                variant="ghost"
                data-testid="blueprint-open"
                onClick={() => props.onOpen()}
              >
                {BLUEPRINT_CARD_COPY.open}
              </DenButton>
            </div>
          </Show>
        }
      >
        <Show
          when={revising()}
          fallback={
            <div class="den-blueprint-inline-card__actions" data-testid="blueprint-card-actions">
              <span class="den-blueprint-inline-card__phase" data-testid="blueprint-card-phase">
                {props.view.phaseLabel}
              </span>
              <DenButton
                variant="ghost"
                data-testid="blueprint-revise"
                onClick={() => setRevising(true)}
              >
                {BLUEPRINT_CARD_COPY.requestChanges}
              </DenButton>
              <For each={[...(props.choiceTransitions ?? [])]}>
                {(arm) => (
                  <DenButton
                    variant="secondary"
                    data-testid={`blueprint-choice-${arm.id}`}
                    disabled={!arm.armed}
                    onClick={() => props.onChoiceTransition?.(arm.id)}
                  >
                    {arm.label}
                  </DenButton>
                )}
              </For>
              <DenButton
                variant="secondary"
                data-testid="blueprint-open"
                onClick={() => props.onOpen()}
              >
                {BLUEPRINT_CARD_COPY.open}
              </DenButton>
              <DenButton
                variant="secondary"
                data-testid="blueprint-reject"
                onClick={() => props.onReject?.()}
              >
                {BLUEPRINT_CARD_COPY.reject}
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="blueprint-approve"
                disabled={!props.view.canApprove}
                onClick={() => props.onApprove?.()}
              >
                {BLUEPRINT_CARD_COPY.approve}
              </DenButton>
            </div>
          }
        >
          <div class="den-blueprint-inline-card__revise" data-testid="blueprint-revise-form">
            <textarea
              class="den-blueprint-inline-card__revise-input"
              data-testid="blueprint-revise-input"
              placeholder="Describe what to change…"
              rows={3}
              value={reviseText()}
              onInput={(e) => setReviseText(e.currentTarget.value)}
              ref={(el) => queueMicrotask(() => focusWithoutScroll(el))}
            />
            <div class="den-blueprint-inline-card__actions" data-testid="blueprint-card-actions">
              <span class="den-blueprint-inline-card__phase" data-testid="blueprint-card-phase">
                {props.view.phaseLabel}
              </span>
              <DenButton
                variant="primary"
                data-testid="blueprint-revise-send"
                disabled={!reviseText().trim()}
                onClick={submitRevision}
              >
                Send
              </DenButton>
              <DenButton
                variant="ghost"
                data-testid="blueprint-revise-cancel"
                onClick={() => {
                  setRevising(false);
                  setReviseText("");
                }}
              >
                Cancel
              </DenButton>
            </div>
          </div>
        </Show>
      </Show>
    </article>
  );
}
