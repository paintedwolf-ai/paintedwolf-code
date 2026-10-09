import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { Show, createSignal } from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import type { JSX } from "solid-js";
import {
  createModalFocusTrap,
  shellChromeInertTargets,
} from "../../platform/interaction/modal-focus-trap.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ChromeCloseButton } from "../shell/ChromeCloseButton.tsx";
import { MarkdownBody } from "../transcript/MarkdownBody.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { openAppLink, isAppLink, confirmAndOpenExternalLink } from "../../platform/desktop/external-link.ts";
import { REPOSITORY_URL, releasePageUrl } from "../../../shared/brand.ts";

export type WhatsNewDialogProps = {
  open: boolean;
  version: string;
  notes: string;
  busy?: boolean;
  onClose: () => void;
  onGotIt: () => void;
};

/** Full release notes, opened from Home's compact update notice. */
export function WhatsNewDialog(props: WhatsNewDialogProps): JSX.Element {
  const [dialogEl, setDialogEl] = createSignal<HTMLDivElement | undefined>();
  const [copied, setCopied] = createSignal(false);
  const close = () => {
    if (!props.busy) props.onClose();
  };

  createModalFocusTrap(
    () => props.open,
    dialogEl,
    {
      onEscape: close,
      inertTarget: () => shellChromeInertTargets(),
    },
  );

  return (
    <Show when={props.open}>
      <ResidentPortal mount={document.body}>
        <div
          class="den-dialog-backdrop whats-new-dialog-backdrop"
          data-testid="whats-new-dialog-backdrop"
          onClick={(event) => {
            if (event.target === event.currentTarget) close();
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <section
            ref={setDialogEl}
            class="den-dialog whats-new-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="whats-new-dialog-title"
            data-testid="whats-new-dialog"
          >
            <header class="den-dialog__header whats-new-dialog__header" {...chromeProps()}>
              <div>
                <p class="whats-new-dialog__eyebrow">Release notes</p>
                <h2 id="whats-new-dialog-title">What’s new in {props.version}</h2>
              </div>
              <ChromeCloseButton
                class="den-dialog__close"
                label="Close release notes"
                testId="whats-new-dialog-close"
                onClick={close}
              />
            </header>

            <Scrollport
              class="den-dialog__body"
              contentClass="whats-new-dialog__body"
              data-testid="whats-new-dialog-notes"
            >
              <MarkdownBody untrusted source={props.notes} onExternalLink={(href) =>
                isAppLink(href) ? openAppLink(href) : confirmAndOpenExternalLink(href)
              } />
              <p class="whats-new-dialog__share" data-testid="whats-new-dialog-share">
                If this release helps you, the best thanks is telling a colleague
                who would use it, or starring the project on GitHub.
              </p>
            </Scrollport>

            <footer class="den-dialog__footer">
              <div class="den-dialog__footer-start">
                <DenButton
                  variant="ghost"
                  data-testid="whats-new-dialog-copy-link"
                  onClick={() => {
                    void writeClipboardText(releasePageUrl(props.version)).then(
                      () => setCopied(true),
                      () => setCopied(false),
                    );
                  }}
                >
                  {copied() ? "Link copied" : "Copy release link"}
                </DenButton>
                <DenButton
                  variant="ghost"
                  data-testid="whats-new-dialog-star"
                  onClick={() => void openAppLink(REPOSITORY_URL)}
                >
                  Star on GitHub
                </DenButton>
              </div>
              <DenButton
                variant="ghost"
                data-testid="whats-new-dialog-close-footer"
                disabled={props.busy}
                onClick={close}
              >
                Close
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="whats-new-dialog-got-it"
                disabled={props.busy}
                onClick={() => props.onGotIt()}
              >
                Got it
              </DenButton>
            </footer>
          </section>
        </div>
      </ResidentPortal>
    </Show>
  );
}
