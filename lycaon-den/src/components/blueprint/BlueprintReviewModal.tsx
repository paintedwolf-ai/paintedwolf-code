import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import type { ProjectRoot } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import {
  blueprintPreviewSource,
  type BlueprintReviewView,
} from "../../blueprint/blueprint-workspace-modal.ts";
import {
  BLUEPRINT_CARD_COPY,
  type BlueprintChoiceTransition,
} from "../../blueprint/blueprint-inline-card-model.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";
import {
  ProjectFilesView,
  type ProjectFileDocumentSession,
} from "../../files/components/ProjectFilesView.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { chromeProps, proseProps } from "../../styling/ui-chrome.ts";
import { focusWithoutScroll } from "../../platform/interaction/focus.ts";

/** Opens the same buffer on the Files stage. */
const OPEN_IN_FILES_LABEL = "Open in files";

type Props = {
  reviewOpen: boolean;
  initialView: BlueprintReviewView;
  selectedBlueprintPath: string | null;
  blueprintName: string | null;
  projectId: string;
  appStore: AppStore;
  roots: ProjectRoot[];
  error: string | null;
  warning: string | null;
  awaitingApproval: boolean;
  canApprove: boolean;
  changed: boolean;
  choiceTransitions: readonly BlueprintChoiceTransition[];
  onClose: () => void;
  onOpenInFiles: () => void;
  onDocumentSessionChange: (
    session: ProjectFileDocumentSession | null,
  ) => void;
  onApprove: () => void;
  onChoiceTransition: (transitionId: string) => void;
  onRequestChanges: (text: string) => boolean | void | Promise<boolean | void>;
};

/** Full-screen approval chrome around one Files document. */
export function BlueprintReviewModal(props: Props) {
  const open = () => props.reviewOpen && Boolean(props.selectedBlueprintPath);
  const documentTarget = createMemo(() => {
    const path = props.selectedBlueprintPath?.trim();
    const root =
      props.roots.find((entry) => entry.is_primary) ?? props.roots[0];
    return path && root ? { path, rootId: root.id } : null;
  });
  const [modalEl, setModalEl] = createSignal<HTMLElement | undefined>();
  const [changesOpen, setChangesOpen] = createSignal(false);
  const [changesText, setChangesText] = createSignal("");
  const [changesSending, setChangesSending] = createSignal(false);

  createModalFocusTrap(open, modalEl, {
    onEscape: () => {
      props.onClose();
    },
    inertTarget: () => [
      ...document.querySelectorAll<HTMLElement>(
        ".den-shell-aside, .den-shell-header, .den-chat-composer-dock",
      ),
    ],
  });

  createEffect(() => {
    if (!open()) {
      setChangesOpen(false);
      setChangesText("");
      setChangesSending(false);
    }
  });

  const footerLead = () => {
    if (!props.awaitingApproval) return null;
    return props.changed
      ? "Blueprint changed — approve this revision to continue."
      : "Approving accepts this blueprint and continues the workflow.";
  };

  const submitChanges = async () => {
    const text = changesText().trim();
    if (!text || changesSending()) return;
    setChangesSending(true);
    try {
      const accepted = await props.onRequestChanges(text);
      if (accepted === false) return;
      setChangesText("");
      setChangesOpen(false);
    } finally {
      setChangesSending(false);
    }
  };

  return (
    <Show when={open()}>
      <div
        class="den-blueprint-workspace-modal"
        data-testid="blueprint-review-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="blueprint-review-modal-title"
        ref={setModalEl}
      >
        <header class="den-blueprint-workspace-modal-header" {...chromeProps()}>
          <div class="den-blueprint-workspace-modal-header-text">
            <h2 id="blueprint-review-modal-title">
              {props.blueprintName ?? "Blueprint"}
            </h2>
            <p class="den-blueprint-workspace-modal-status">
              {/* Path stays selectable under the header chrome. */}
              <span
                class="den-blueprint-workspace-modal-path-inline"
                data-testid="blueprint-review-path"
                {...proseProps()}
              >
                {props.selectedBlueprintPath}
              </span>
            </p>
          </div>
          <div class="den-blueprint-workspace-modal-header-tools">
            <DenButton
              variant="secondary"
              compact
              data-testid="blueprint-review-open-files"
              disabled={!props.selectedBlueprintPath}
              onClick={props.onOpenInFiles}
            >
              {OPEN_IN_FILES_LABEL}
            </DenButton>
            <button
              type="button"
              class="den-blueprint-workspace-modal-close den-inset-icon-btn"
              onClick={props.onClose}
              data-testid="blueprint-review-close"
              aria-label="Close blueprint review"
            >
              ×
            </button>
          </div>
        </header>

        <section
          class="den-blueprint-workspace-modal-body"
          data-testid="blueprint-review-body"
        >
          <Show when={props.error}>
            <p
              class="den-blueprint-workspace-modal-alert den-blueprint-workspace-modal-alert--error"
              role="alert"
            >
              {props.error}
            </p>
          </Show>
          <Show when={props.warning}>
            <p
              class="den-blueprint-workspace-modal-alert den-blueprint-workspace-modal-alert--warn"
              role="status"
            >
              {props.warning}
            </p>
          </Show>
          <Show
            keyed
            when={documentTarget()}
            fallback={
              <p class="den-blueprint-workspace-modal-loading" role="alert">
                The blueprint file is unavailable.
              </p>
            }
          >
            {(target) => (
              <ProjectFilesView
                projectId={props.projectId}
                appStore={props.appStore}
                roots={props.roots}
                document={{
                  rootId: target.rootId,
                  path: target.path,
                  initialMarkdownView: props.initialView,
                  previewSource: blueprintPreviewSource,
                  onSessionChange: props.onDocumentSessionChange,
                }}
              />
            )}
          </Show>
        </section>

        <Scrollport
          class="den-blueprint-workspace-modal-footer den-blueprint-review-footer"
          contentAs="footer"
          contentClass="den-blueprint-review-footer__content"
        >
          <Show
            when={props.awaitingApproval}
            fallback={
              <div class="den-blueprint-review-footer__row">
                <span class="den-blueprint-review-footer__lead" />
                <div class="den-blueprint-review-footer__actions">
                  <DenButton
                    variant="primary"
                    data-testid="blueprint-review-done"
                    onClick={props.onClose}
                  >
                    Done
                  </DenButton>
                </div>
              </div>
            }
          >
            <Show when={changesOpen()}>
              <div
                class="den-blueprint-review-footer__changes"
                data-testid="blueprint-review-changes-form"
              >
                <textarea
                  class="den-blueprint-review-footer__changes-input"
                  data-testid="blueprint-review-changes-input"
                  placeholder="Describe what to change — sent to the planner in chat"
                  rows={2}
                  value={changesText()}
                  onInput={(event) => setChangesText(event.currentTarget.value)}
                  ref={(element) =>
                    queueMicrotask(() => focusWithoutScroll(element))
                  }
                />
                <div class="den-blueprint-review-footer__changes-actions">
                  <DenButton
                    variant="primary"
                    data-testid="blueprint-review-changes-send"
                    disabled={!changesText().trim() || changesSending()}
                    onClick={() => void submitChanges()}
                  >
                    {changesSending() ? "Sending…" : "Send"}
                  </DenButton>
                  <DenButton
                    variant="ghost"
                    data-testid="blueprint-review-changes-cancel"
                    disabled={changesSending()}
                    onClick={() => {
                      setChangesOpen(false);
                      setChangesText("");
                    }}
                  >
                    Cancel
                  </DenButton>
                </div>
              </div>
            </Show>
            <div class="den-blueprint-review-footer__row">
              <span
                class="den-blueprint-review-footer__lead"
                data-testid="blueprint-review-lead"
              >
                {footerLead()}
              </span>
              <div class="den-blueprint-review-footer__actions">
                <DenButton
                  variant="secondary"
                  data-testid="blueprint-review-request-changes"
                  onClick={() => setChangesOpen((value) => !value)}
                >
                  {BLUEPRINT_CARD_COPY.requestChanges}
                </DenButton>
                <For each={[...props.choiceTransitions]}>
                  {(transition) => (
                    <DenButton
                      variant="secondary"
                      data-testid={`blueprint-review-choice-${transition.id}`}
                      disabled={!transition.armed}
                      onClick={() => props.onChoiceTransition(transition.id)}
                    >
                      {transition.label}
                    </DenButton>
                  )}
                </For>
                <DenButton
                  variant="primary"
                  data-testid="blueprint-review-approve"
                  disabled={!props.canApprove}
                  onClick={props.onApprove}
                >
                  {BLUEPRINT_CARD_COPY.approve}
                </DenButton>
              </div>
            </div>
          </Show>
        </Scrollport>
      </div>
    </Show>
  );
}
