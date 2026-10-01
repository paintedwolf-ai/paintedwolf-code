import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { PreparedSurface } from "../../primitives/PreparedSurface.tsx";
import { Show, createResource, createSignal } from "solid-js";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import type { SecretMarkPreview } from "../../../api/types.ts";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { markFailureMessage } from "./mark-secret-failure.ts";
import { SECRET_SPAN_COPY } from "./secret-span-copy.ts";

export type MarkSecretTarget = {
  path: string;
  line: number;
  dirty: boolean;
  preview: (trim: boolean) => Promise<SecretMarkPreview>;
  mark: (args: { name: string; purpose: string; trim: boolean }) => Promise<string>;
};

type Props = {
  target: MarkSecretTarget | null;
  onClose: () => void;
  onMarked: (name: string) => void;
  onError: (message: string) => void;
};

export function MarkSecretDialog(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  const [name, setName] = createSignal("");
  const [purpose, setPurpose] = createSignal("");
  const [trim, setTrim] = createSignal(true);
  const [submitting, setSubmitting] = createSignal(false);

  createModalFocusTrap(() => props.target != null, () => dialogEl, {
    onEscape: () => close(),
  });

  const close = () => {
    setName("");
    setPurpose("");
    setTrim(true);
    setSubmitting(false);
    props.onClose();
  };

  const [previewResource] = createResource(
    () => {
      const target = props.target;
      return target ? { target, trim: trim() } : null;
    },
    async (args) => args.target.preview(args.trim),
  );
  // Guard the throwing resource accessor so preview errors stay in this sheet.
  const previewError = () => previewResource.error;
  const preview = () =>
    previewResource.error === undefined ? previewResource() : undefined;

  const canSubmit = () =>
    !submitting() &&
    name().trim().length > 0 &&
    purpose().trim().length > 0 &&
    preview()?.eligible === true;

  const submit = async () => {
    const target = props.target;
    if (!target || !canSubmit()) return;
    setSubmitting(true);
    try {
      const marked = await target.mark({
        name: name().trim(),
        purpose: purpose().trim(),
        trim: trim(),
      });
      props.onMarked(marked);
      close();
    } catch (error) {
      setSubmitting(false);
      props.onError(markFailureMessage(error));
    }
  };

  return (
    <Show when={props.target} keyed>
      {(target) => (
        <ResidentPortal mount={document.body}>
          <div
            class="den-dialog-backdrop den-dialog-backdrop--viewport"
            onClick={(e) => {
              if (e.target === e.currentTarget) close();
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={dialogEl}
              class="den-dialog max-w-[460px]"
              role="dialog"
              aria-modal="true"
              aria-labelledby="mark-secret-title"
              data-testid="mark-secret-dialog"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="mark-secret-title">{SECRET_SPAN_COPY.sheetTitle}</h2>
              </header>
              {/* The lede is static context, so it sits above the prepared body rather than
                  inside it, and stays out of the scroller the layout contract pins. */}
              <p class="den-dialog__hint" data-testid="mark-secret-origin">
                {SECRET_SPAN_COPY.sheetLede(target.path, target.line)}
                <Show when={target.dirty}>
                  {" · "}
                  {SECRET_SPAN_COPY.sheetFromDraft}
                </Show>
              </p>

              <PreparedSurface name="secret-preview" ready={() => !previewResource.loading}>
              <Scrollport class="den-dialog__body" contentClass="flex flex-col gap-3 pt-1 pb-4">
                <div class="den-mark-secret__receipt" data-testid="mark-secret-receipt">
                  <Show
                    when={preview()}
                    fallback={
                      <Show
                        when={previewError() !== undefined}
                        fallback={<p class="den-dialog__hint">{SECRET_SPAN_COPY.loading}</p>}
                      >
                        <p
                          class="den-mark-secret__refusal"
                          role="alert"
                          data-testid="mark-secret-preview-error"
                        >
                          {markFailureMessage(previewError())}
                        </p>
                      </Show>
                    }
                    keyed
                  >
                    {(shot) => (
                      <Show
                        when={shot.eligible}
                        fallback={
                          <p class="den-mark-secret__refusal" role="alert">
                            {shot.reason === "already_protected"
                              ? SECRET_SPAN_COPY.sheetAlreadyProtected
                              : SECRET_SPAN_COPY.sheetCaptureFailed}
                          </p>
                        }
                      >
                        <div class="den-mark-secret__line">
                          <span class="den-mark-secret__key">
                            {SECRET_SPAN_COPY.sheetCapturing}
                          </span>
                          <span class="den-mark-secret__value">
                            {shot.shape || `${shot.rune_length} characters`}
                          </span>
                        </div>
                        <Show
                          when={
                            (shot.trimmed_leading ?? 0) +
                                (shot.trimmed_trailing ?? 0) >
                              0 || !trim()
                          }
                        >
                          <button
                            type="button"
                            class="den-mark-secret__trim"
                            data-testid="mark-secret-trim-toggle"
                            onClick={() => setTrim((on) => !on)}
                          >
                            <Show
                              when={trim()}
                              fallback={SECRET_SPAN_COPY.sheetRestoreTrim}
                            >
                              {SECRET_SPAN_COPY.sheetTrimmed(
                                (shot.trimmed_leading ?? 0) +
                                  (shot.trimmed_trailing ?? 0),
                              )}
                              {" · "}
                              {SECRET_SPAN_COPY.sheetKeepTrim}
                            </Show>
                          </button>
                        </Show>
                      </Show>
                    )}
                  </Show>
                </div>

                <label class="den-mark-secret__field">
                  <span>{SECRET_SPAN_COPY.sheetName}</span>
                  <input
                    type="text"
                    maxlength="80"
                    autofocus
                    data-testid="mark-secret-name"
                    placeholder={SECRET_SPAN_COPY.sheetNamePlaceholder}
                    value={name()}
                    onInput={(e) => setName(e.currentTarget.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && canSubmit()) void submit();
                    }}
                  />
                </label>

                <label class="den-mark-secret__field">
                  <span>{SECRET_SPAN_COPY.sheetPurpose}</span>
                  <input
                    type="text"
                    maxlength="240"
                    data-testid="mark-secret-purpose"
                    placeholder={SECRET_SPAN_COPY.sheetPurposePlaceholder}
                    value={purpose()}
                    onInput={(e) => setPurpose(e.currentTarget.value)}
                  />
                  <small>{SECRET_SPAN_COPY.sheetPurposeHint}</small>
                </label>

                <p class="den-dialog__hint">{SECRET_SPAN_COPY.sheetScopeHint}</p>
                <p class="den-mark-secret__guarantee">
                  {SECRET_SPAN_COPY.sheetNoEditHint}
                </p>
              </Scrollport>

              </PreparedSurface>
              <footer class="den-dialog__footer">
                <DenButton variant="ghost" data-testid="mark-secret-cancel" onClick={close}>
                  {SECRET_SPAN_COPY.sheetCancel}
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="mark-secret-submit"
                  disabled={!canSubmit()}
                  onClick={() => void submit()}
                >
                  <Show when={submitting()} fallback={SECRET_SPAN_COPY.sheetSubmit}>
                    {SECRET_SPAN_COPY.sheetSubmitting}
                  </Show>
                </DenButton>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      )}
    </Show>
  );
}
