import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { ProjectRemovalAssessment, ProjectRemovalCheck, ProjectRemovalReason } from "../../api/types.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";

type Review = {
  assessment: ProjectRemovalAssessment | null;
  projectName: string;
  resolve: (ids: string[] | null) => void;
};

export function createProjectRemovalReview() {
  const [pending, setPending] = createSignal<Review | null>(null);
  const queue: Review[] = [];
  let disposed = false;
  const settle = (ids: string[] | null) => {
    pending()?.resolve(ids);
    setPending(queue.shift() ?? null);
  };
  onCleanup(() => {
    disposed = true;
    pending()?.resolve(null);
    for (const request of queue.splice(0)) request.resolve(null);
  });
  return {
    pending,
    settle,
    show: (assessment: ProjectRemovalAssessment | null, projectName = "this project"): Promise<string[] | null> => {
      if (disposed) return Promise.resolve(null);
      return new Promise((resolve) => {
        const request = { assessment, projectName, resolve };
        if (pending()) queue.push(request);
        else setPending(request);
      });
    },
  };
}

const reasonLabels: Record<
  ProjectRemovalReason["code"],
  (subject: string, assessment: ProjectRemovalAssessment) => string
> = {
  project_suggestion: (subject, assessment) =>
    `Suggested by ${assessment.checks.find((check) => check.project_id === subject)?.name || "another project"}`,
  extension_dependency: (subject) => `Required by ${subject}`,
  device_selection: (subject) => `Selected for ${subject}`,
  device_configuration: () => "Has device configuration",
  stock_extension: () => "Bundled with the app",
  incomplete_project_evidence: () => "Some project references could not be checked",
};

const checkReasons: Record<NonNullable<ProjectRemovalCheck["reason"]>, string> = {
  suggestions_disabled: "Suggested extensions are turned off",
  overlay_unavailable: "Project configuration is unavailable",
  overlay_incompatible: "Project configuration could not be read",
  root_unavailable: "The project folder is unavailable",
  manifest_unreadable: "Extension suggestions could not be read",
  manifest_invalid: "Extension suggestions are invalid",
};

export function ProjectRemovalDialog(props: { review: ReturnType<typeof createProjectRemovalReview> }) {
  let dialog: HTMLDivElement | undefined;
  const [selected, setSelected] = createSignal<string[]>([]);
  createEffect(() => {
    props.review.pending();
    setSelected([]);
  });
  createModalFocusTrap(() => props.review.pending() != null, () => dialog, {
    onEscape: () => props.review.settle(null),
  });
  return (
    <Show when={props.review.pending()} keyed>{(request) => (
      <ResidentPortal mount={document.body}>
        <div class="den-dialog-backdrop">
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={dialog}
            class="den-dialog den-dialog--wide"
            role="dialog"
            aria-modal="true"
            aria-labelledby="project-removal-title"
            data-testid="project-removal-dialog"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="project-removal-title">Delete project</h2>
            </header>
            <Scrollport class="den-dialog__body">
              <p class="den-dialog__hint">History and any unsaved draft workspace for <strong>{request.projectName}</strong> will be deleted. Attached folders stay on disk.</p>
              <Show
                when={request.assessment}
                fallback={<p class="den-dialog__hint" role="status">Extension references could not be assessed. All extensions will be kept.</p>}
                keyed
              >{(assessment) => <>
                <Show when={assessment.extensions.length > 0}>
                  <p class="den-dialog__hint">These extensions were installed from this project. They are shared across this device. Choose which to remove after the project is deleted.</p>
                  <For each={assessment.extensions}>{(pack, index) => (
                    <div>
                      <DenCheckbox
                        class="den-dialog__check"
                        checked={selected().includes(pack.pack_id)}
                        disabled={pack.disposition !== "eligible"}
                        aria-describedby={`project-removal-reason-${index()}`}
                        onChange={(event) => setSelected((ids) => event.currentTarget.checked
                          ? [...ids, pack.pack_id]
                          : ids.filter((id) => id !== pack.pack_id))}
                      >
                        {pack.pack_id}
                      </DenCheckbox>
                      <p id={`project-removal-reason-${index()}`} class="den-dialog__hint">
                        {pack.disposition === "eligible"
                          ? "No declared references found in the checked sources."
                          : pack.reasons.map((reason) => reasonLabels[reason.code](reason.subject_id, assessment)).join(". ")}
                      </p>
                    </div>
                  )}</For>
                  <Show when={!assessment.complete}>
                    <p class="den-dialog__hint">Some references are unknown. Extensions without enough evidence will be kept.</p>
                    <ul class="den-dialog__hint">
                      <For each={assessment.checks.filter((check) => check.state === "unknown")}>{(check) => (
                        <li>{check.name || "Project"}<Show when={check.reason} keyed>{(reason) => `: ${checkReasons[reason]}`}</Show></li>
                      )}</For>
                    </ul>
                  </Show>
                  <p class="den-dialog__hint">Checks cover other projects’ extension suggestions, extension dependencies, and device settings. Extensions may still be in use outside these sources. If these checks change, your extensions will be kept.</p>
                </Show>
              </>}</Show>
            </Scrollport>
            <footer class="den-dialog__footer">
              <div class="den-dialog__footer-end">
                <DenButton variant="ghost" autofocus onClick={() => props.review.settle(null)}>Cancel</DenButton>
                <DenButton variant="danger" onClick={() => props.review.settle(selected())}>
                  {selected().length > 0
                    ? `Delete project and remove ${selected().length} extension${selected().length === 1 ? "" : "s"}`
                    : request.assessment && request.assessment.extensions.length === 0
                      ? "Delete project"
                      : "Delete project and keep extensions"}
                </DenButton>
              </div>
            </footer>
          </div>
        </div>
      </ResidentPortal>
    )}</Show>
  );
}
