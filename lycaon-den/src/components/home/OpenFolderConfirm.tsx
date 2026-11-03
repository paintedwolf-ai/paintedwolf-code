import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { FolderDetect, FolderDetectProject } from "../../api/types.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { EXTENSION_SUGGESTIONS_COPY } from "../../settings/extensions/extension-suggestions-copy.ts";
import { hasInstallableExtensionSuggestions } from "../../settings/extensions/extension-suggestions-model.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ExtensionSuggestionsBlock } from "./ExtensionSuggestionsBlock.tsx";

export type OpenFolderChoice = {
  installIds: string[];
  projectId?: string | null;
};

export function detectedFolderProjects(
  detect: FolderDetect | null | undefined,
): FolderDetectProject[] {
  if (!detect) return [];
  if (detect.projects && detect.projects.length > 0) return detect.projects;
  if (detect.project_id) {
    return [
      {
        id: detect.project_id,
        name: folderName(detect.path),
        roots: [],
        is_single_root: true,
        last_opened_at: "",
      },
    ];
  }
  return [];
}

export function defaultOpenFolderProjectId(
  detect: FolderDetect | null | undefined,
): string | null {
  const single = detectedFolderProjects(detect).find((p) => p.is_single_root);
  return single ? single.id : null;
}

type Props = {
  open: boolean;
  path: string | null;
  detect: FolderDetect | null;
  busy?: boolean;
  onConfirm: (choice: OpenFolderChoice) => void;
  onClose: () => void;
};

/** Delay before backdrop dismissal is enabled. */
export const OPEN_FOLDER_BACKDROP_ARM_MS = 350;

export function OpenFolderConfirm(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  const [installIds, setInstallIds] = createSignal<string[]>([]);
  const [selectedProjectId, setSelectedProjectId] = createSignal<string | null>(
    defaultOpenFolderProjectId(props.detect),
  );
  const [backdropArmed, setBackdropArmed] = createSignal(false);
  const titlePath = () => props.detect?.path ?? props.path ?? "";
  const existingProjects = () => detectedFolderProjects(props.detect);
  const hasExistingProjects = () => existingProjects().length > 0;
  const suggestions = () => props.detect?.extension_suggestions ?? null;
  const hasInstalls = () => hasInstallableExtensionSuggestions(suggestions());

  createEffect(() => {
    if (!props.open || props.path == null) {
      setBackdropArmed(false);
      return;
    }
    setSelectedProjectId(defaultOpenFolderProjectId(props.detect));
    setInstallIds([]);
    setBackdropArmed(false);
    const timer = window.setTimeout(() => setBackdropArmed(true), OPEN_FOLDER_BACKDROP_ARM_MS);
    onCleanup(() => window.clearTimeout(timer));
  });

  createModalFocusTrap(
    () => props.open && props.path != null,
    () => dialogEl,
    {
      onEscape: () => {
        if (!props.busy) props.onClose();
      },
    },
  );

  const dialogTitle = () => {
    const name = folderName(titlePath());
    return hasExistingProjects()
      ? `Open ${name}`
      : EXTENSION_SUGGESTIONS_COPY.dialogTitle(name);
  };

  const primaryLabel = () => {
    if (props.busy) return "Opening…";
    const n = installIds().length;
    if (n > 0) {
      return EXTENSION_SUGGESTIONS_COPY.installAndOpen(n, "open");
    }
    if (hasExistingProjects() && selectedProjectId() === null) {
      return "Create new project";
    }
    return EXTENSION_SUGGESTIONS_COPY.openAction;
  };

  const dismissFromBackdrop = (e: MouseEvent) => {
    if (e.target !== e.currentTarget) return;
    // Ignore the click that opened the dialog.
    if (props.busy || !backdropArmed()) return;
    props.onClose();
  };

  return (
    <Show when={props.open && props.path ? props.path : null} keyed>
      {(path) => (
        <div class="den-dialog-backdrop" data-testid="open-folder-backdrop" onClick={dismissFromBackdrop}>
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={dialogEl}
            class="den-dialog"
            classList={{ "den-dialog--wide": hasInstalls() || hasExistingProjects() }}
            role="dialog"
            aria-modal="true"
            aria-labelledby="open-folder-title"
            data-testid="open-folder-confirm"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="open-folder-title">{dialogTitle()}</h2>
            </header>
            <Scrollport class="den-dialog__body">
              <p class="den-dialog__hint" data-testid="open-folder-path">
                {path}
              </p>

              <Show when={hasExistingProjects()}>
                <div
                  role="radiogroup"
                  aria-label="Project choice"
                  class="flex flex-col gap-1.5 px-4 pb-3"
                  data-testid="open-folder-project-choices"
                >
                  <For each={existingProjects()}>
                    {(project) => (
                      <button
                        type="button"
                        role="radio"
                        aria-checked={selectedProjectId() === project.id}
                        class="den-dialog__row"
                        data-testid={`open-folder-choice-project-${project.id}`}
                        disabled={props.busy}
                        onClick={() => setSelectedProjectId(project.id)}
                      >
                        <span class="den-dialog__row-title">
                          Open existing project “{project.name}”
                        </span>
                        <span class="den-dialog__row-sub">
                          {projectChoiceSummary(project, path)}
                        </span>
                      </button>
                    )}
                  </For>
                  <button
                    type="button"
                    role="radio"
                    aria-checked={selectedProjectId() === null}
                    class="den-dialog__row"
                    data-testid="open-folder-choice-new"
                    disabled={props.busy}
                    onClick={() => setSelectedProjectId(null)}
                  >
                    <span class="den-dialog__row-title">
                      Create a new project for {folderName(titlePath())}
                    </span>
                    <span class="den-dialog__row-sub">
                      Fresh single-folder project · {path}
                    </span>
                  </button>
                </div>
              </Show>

              <Show when={suggestions()} keyed>
                {(value) => (
                  <ExtensionSuggestionsBlock
                    response={value}
                    onSelectionChange={setInstallIds}
                  />
                )}
              </Show>
            </Scrollport>

            <footer class="den-dialog__footer">
              <div class="den-dialog__footer-end">
                <DenButton
                  variant="ghost"
                  data-testid="open-folder-cancel"
                  disabled={props.busy}
                  onClick={() => props.onClose()}
                >
                  {EXTENSION_SUGGESTIONS_COPY.cancel}
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="open-folder-confirm-submit"
                  disabled={props.busy}
                  onClick={() =>
                    props.onConfirm({
                      installIds: installIds(),
                      projectId: selectedProjectId(),
                    })
                  }
                >
                  {primaryLabel()}
                </DenButton>
              </div>
            </footer>
          </div>
        </div>
      )}
    </Show>
  );
}

function projectChoiceSummary(project: FolderDetectProject, fallbackPath: string): string {
  if (project.is_single_root) {
    return project.roots[0]?.path || fallbackPath;
  }
  const paths = project.roots.map((r) => r.path).filter(Boolean);
  return paths.length > 0
    ? `${paths.length} folders: ${paths.join(", ")}`
    : "Multi-folder project";
}

function folderName(path: string): string {
  const parts = path.replace(/[/\\]+$/, "").split(/[/\\]/);
  return parts[parts.length - 1] || path;
}
