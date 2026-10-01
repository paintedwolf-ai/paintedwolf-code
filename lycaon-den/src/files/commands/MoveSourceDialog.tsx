import { ChromeDragSurface } from "../../components/shell/ChromeDragSurface.tsx";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { PreparedSurface } from "../../components/primitives/PreparedSurface.tsx";
import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import { ResidentPortal } from "../../components/primitives/ResidentPortal.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import type { ProjectRoot, SourceDirListing } from "../../api/types.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { childTreePath, fileDisplayName } from "../components/project-files-model.ts";

export type MoveSourceState = {
  rootId: string;
  path: string;
  isDir: boolean;
};

type Props = {
  projectId: string;
  state: MoveSourceState | null;
  roots: ProjectRoot[];
  browse: (rootId: string, dir: string) => Promise<SourceDirListing>;
  busy: boolean;
  error: string | null;
  onCancel: () => void;
  onMove: (dir: string) => void;
};

function parentDir(path: string): string {
  const index = path.lastIndexOf("/");
  return index < 0 ? "." : path.slice(0, index) || ".";
}

function crumbs(dir: string, rootLabel: string): { label: string; path: string }[] {
  if (dir === ".") return [{ label: `@${rootLabel}`, path: "." }];
  const parts = dir.split("/");
  return [
    { label: `@${rootLabel}`, path: "." },
    ...parts.map((label, index) => ({
      label,
      path: parts.slice(0, index + 1).join("/"),
    })),
  ];
}

export function MoveSourceDialog(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  const [dir, setDir] = createSignal(".");
  const [query, setQuery] = createSignal("");
  const destination = createSurfaceQuery({
    name: "move-source-destination",
    source: () => props.state ? { client: props.browse, key: JSON.stringify([props.state.rootId, dir()]), rootId: props.state.rootId, dir: dir(), subject: props.state.path } : null,
    scope: (source) => JSON.stringify([source.rootId, source.subject]),
    load: ({ client, rootId, dir }) => client(rootId, dir),
    required: false,
  });
  const listing = destination.value;
  const loading = destination.loading;
  const loadError = destination.error;
  observeSurfaceFailure(
    { code: "files_folder_listing_unavailable", title: "Could not list folders", suggestedAction: "Pick another folder or cancel the move, then try again." },
    loadError,
    () => props.projectId,
  );
  const displayedDir = () => destination.displayed()?.source.dir ?? dir();

  createModalFocusTrap(() => props.state != null, () => dialogEl, {
    onEscape: () => {
      props.onCancel();
    },
  });

  createEffect(() => {
    const state = props.state;
    if (!state) return;
    setDir(parentDir(state.path));
    setQuery("");
  });

  const folders = createMemo(() => {
    const needle = query().trim().toLocaleLowerCase();
    return (listing()?.entries ?? []).filter((entry) =>
      entry.is_dir && (!needle || entry.name.toLocaleLowerCase().includes(needle))
    );
  });

  const destinationValid = createMemo(() => {
    const state = props.state;
    if (!state) return false;
    if (dir() === parentDir(state.path)) return false;
    return !(state.isDir && (dir() === state.path || dir().startsWith(`${state.path}/`)));
  });
  const selectedRootLabel = () => {
    const root = props.roots.find((entry) => entry.id === props.state?.rootId);
    return root?.label ?? "root";
  };

  return (
    <Show when={props.state} keyed>
      {(state) => (
        <ResidentPortal mount={document.body}>
          <div class="den-dialog-backdrop" onClick={(event) => {
            if (event.target === event.currentTarget) props.onCancel();
          }}>
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={dialogEl}
              class="den-dialog den-move-source-dialog"
              role="dialog"
              aria-modal="true"
              aria-labelledby="files-move-title"
              data-testid="files-move-dialog"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="files-move-title">Move {fileDisplayName(state.path)}</h2>
              </header>
              <PreparedSurface name="destination-folders" ready={() => !destination.coldPending()}>
              <nav class="den-move-source-dialog__crumbs" aria-label="Destination folder">
                <For each={crumbs(displayedDir(), selectedRootLabel())}>{(crumb, index) => (
                  <>
                    <Show when={index() > 0}><span aria-hidden="true">/</span></Show>
                    <button type="button" onClick={() => setDir(crumb.path)}>{crumb.label}</button>
                  </>
                )}</For>
              </nav>
              <label class="den-dialog__label" for="files-move-filter">Filter folders</label>
              <input
                id="files-move-filter"
                class="den-dialog__field"
                type="search"
                value={query()}
                placeholder="Folder name"
                autofocus
                onInput={(event) => setQuery(event.currentTarget.value)}
              />
              <Scrollport
                class="den-dialog__list den-move-source-dialog__list"
                contentClass="den-dialog__list-content"
                content={{ role: "list" }}
              >
                <Show when={displayedDir() !== "."}>
                  <button class="den-dialog__row" type="button" onClick={() => setDir(parentDir(displayedDir()))}>
                    <span class="den-dialog__row-title">Parent folder</span>
                    <span class="den-dialog__row-sub">..</span>
                  </button>
                </Show>
                <For each={folders()}>{(folder) => (
                  <button
                    class="den-dialog__row"
                    type="button"
                    data-testid="files-move-folder"
                    onClick={() => setDir(childTreePath(displayedDir(), folder.name))}
                  >
                    <span class="den-dialog__row-title">{folder.name}</span>
                    <span class="den-dialog__row-sub">Open folder</span>
                  </button>
                )}</For>
                <Show when={destination.showLoading()}><p class="den-dialog__hint" role="status">Loading folders…</p></Show>
                <Show when={!loading() && !loadError() && folders().length === 0}>
                  <p class="den-dialog__hint">No folders here.</p>
                </Show>
              </Scrollport>
              <Show when={props.error}>
                <p class="den-dialog__error" role="alert">{props.error}</p>
              </Show>
              <p class="den-dialog__hint">
                Destination: {displayedDir() === "." ? `@${selectedRootLabel()}` : `@${selectedRootLabel()}/${displayedDir()}`}
              </p>
              </PreparedSurface>
              <footer class="den-dialog__footer">
                <DenButton variant="ghost" onClick={props.onCancel}>{props.busy ? "Continue working" : "Cancel"}</DenButton>
                <DenButton
                  variant="primary"
                  disabled={props.busy || loading() || !destinationValid()}
                  data-testid="files-move-confirm"
                  onClick={() => props.onMove(dir())}
                >
                  {props.busy ? "Moving…" : "Move here"}
                </DenButton>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      )}
    </Show>
  );
}
