import { isAbsolutePath } from "../../platform/files/reveal-in-file-manager.ts";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Show, createSignal } from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import type { JSX } from "solid-js";
import { getBackendConnection } from "../../platform/connection/backend.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { OpenInButton } from "../OpenInButton.tsx";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";
import {
  initialReportBugStep,
  openIssuesPage,
  saveReportBundle,
  type ReportBugOpenIssues,
  type ReportBugSave,
  type ReportBugStep,
} from "../../report-a-bug/report-a-bug.ts";
import { DenButton } from "../primitives/DenButton.tsx";

export type ReportBugDialogProps = {
  open: boolean;
  onClose: () => void;
  save?: ReportBugSave;
  openIssues?: ReportBugOpenIssues;
  getConnection?: typeof getBackendConnection;
};

export function ReportBugDialog(props: ReportBugDialogProps): JSX.Element {
  let dialogEl: HTMLDivElement | undefined;
  const [step, setStep] = createSignal<ReportBugStep>(initialReportBugStep());

  createModalFocusTrap(
    () => props.open,
    () => dialogEl,
    {
      onEscape: () => {
        if (step().kind !== "saving") props.onClose();
      },
    },
  );

  const resetAndClose = () => {
    if (step().kind === "saving") return;
    setStep(initialReportBugStep());
    props.onClose();
  };

  const onSave = async () => {
    setStep({ kind: "saving" });
    const getConnection = props.getConnection ?? getBackendConnection;
    const next = await saveReportBundle(getConnection(), props.save);
    setStep(next);
  };

  const onOpenIssues = () => {
    void openIssuesPage(props.openIssues);
  };

  const explainError = (): string | undefined => {
    const current = step();
    return current.kind === "explain" ? current.error : undefined;
  };

  const savedStep = (): Extract<ReportBugStep, { kind: "saved" }> | undefined => {
    const current = step();
    return current.kind === "saved" ? current : undefined;
  };
  const savedPathTarget = (): LocalPathTarget | null => {
    const saved = savedStep();
    return saved && isAbsolutePath(saved.path)
      ? { absolutePath: saved.path, projectRoots: [saved.path], entryKind: "file", origin: "device" }
      : null;
  };

  return (
    <Show when={props.open}>
      <ResidentPortal mount={document.body}>
        <div
          class="den-dialog-backdrop"
          onClick={(e) => {
            if (e.target === e.currentTarget && step().kind !== "saving") {
              resetAndClose();
            }
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={dialogEl}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="report-bug-title"
            data-testid="report-bug-dialog"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="report-bug-title">Report a bug</h2>
            </header>

            <Show when={step().kind !== "saved"}>
              <p class="den-dialog__hint" data-testid="report-bug-explain">
                A report is a diagnostics bundle saved on your machine: the
                System information summary, recent logs, and settings with secrets
                removed. Nothing is sent automatically. Open a GitHub issue and
                paste the filename — do not attach the zip.
              </p>
              <Show when={explainError()} keyed>
                {(error) => (
                  <p class="den-dialog__error" data-testid="report-bug-error">
                    {error}
                  </p>
                )}
              </Show>
            </Show>

            <Show when={savedStep()} keyed>
              {(saved) => (
                <>
                  <p class="den-dialog__hint" data-testid="report-bug-saved">
                    Your report bundle was saved to{" "}
                    <code data-testid="report-bug-path">{saved.path}</code>.
                  </p>
                  <p class="den-dialog__hint" data-testid="report-bug-filename">
                    Paste the filename into the report id field. Do not attach
                    the zip.
                  </p>
                </>
              )}
            </Show>

            <footer class="den-dialog__footer">
              <Show when={step().kind !== "saved"}>
                <DenButton
                  variant="ghost"
                  data-testid="report-bug-cancel"
                  disabled={step().kind === "saving"}
                  onClick={() => resetAndClose()}
                >
                  Cancel
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="report-bug-save"
                  disabled={step().kind === "saving"}
                  onClick={() => void onSave()}
                >
                  {step().kind === "saving"
                    ? "Saving…"
                    : "Save report bundle…"}
                </DenButton>
              </Show>
              <Show when={step().kind === "saved"}>
                <OpenInButton target={savedPathTarget()} />
                <Show when={!savedPathTarget()}><span>Find the report in your browser downloads.</span></Show>
                <DenButton
                  variant="primary"
                  data-testid="report-bug-open-issues"
                  onClick={() => onOpenIssues()}
                >
                  Open GitHub Issues
                </DenButton>
                <DenButton
                  variant="ghost"
                  data-testid="report-bug-done"
                  onClick={() => resetAndClose()}
                >
                  Done
                </DenButton>
              </Show>
            </footer>
          </div>
        </div>
      </ResidentPortal>
    </Show>
  );
}
