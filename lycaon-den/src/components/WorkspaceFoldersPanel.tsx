import { contextAction } from "./context-actions.ts";
import { For, Show, createSignal } from "solid-js";
import type { ProjectRoot } from "../api/types.ts";
import { pickProjectFolder } from "../platform/files/folder.ts";
import {
  createModalFocusTrap,
  shellChromeInertTargets,
} from "../platform/interaction/modal-focus-trap.ts";
import { ContextMenu, type ContextMenuAnchor } from "./ContextMenu.tsx";
import { InlineRenameInput } from "./inline-rename/InlineRenameInput.tsx";
import { projectPathMenuItems } from "./project-path-menu-items.ts";
import { DenButton } from "./primitives/DenButton.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";
import { chromeProps } from "../styling/ui-chrome.ts";

const ROOT_LABEL_MAX = 64;

export type WorkspaceFoldersPanelProps = {
  open: boolean;
  projectId: string;
  roots: ProjectRoot[];
  onClose: () => void;
  onAttach: (path: string) => Promise<void>;
  onDetach: (rootId: string) => Promise<void>;
  onSetPrimary: (rootId: string) => Promise<void>;
  onRenameLabel: (rootId: string, label: string) => Promise<void>;
};

export function WorkspaceFoldersPanel(props: WorkspaceFoldersPanelProps) {
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal<string | null>(null);
  const [confirmRemoveId, setConfirmRemoveId] = createSignal<string | null>(null);
  const [renamingRootId, setRenamingRootId] = createSignal<string | null>(null);
  const [panelEl, setPanelEl] = createSignal<HTMLElement | undefined>();
  const [menu, setMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    root: ProjectRoot;
  } | null>(null);

  createModalFocusTrap(
    () => props.open,
    panelEl,
    {
      onEscape: () => {
        if (renamingRootId()) {
          setRenamingRootId(null);
          return;
        }
        props.onClose();
      },
      inertTarget: () => shellChromeInertTargets(),
    },
  );

  const openMenu = (e: MouseEvent, root: ProjectRoot) => {
    e.preventDefault();
    e.stopPropagation();
    setMenu({ anchor: { x: e.clientX, y: e.clientY }, root });
  };

  const run = async (action: () => Promise<void>) => {
    if (busy()) return;
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Request failed");
    } finally {
      setBusy(false);
    }
  };

  const addFolder = async () => {
    try {
      const path = await pickProjectFolder();
      if (!path) return;
      await run(() => props.onAttach(path));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Couldn't open folder picker");
    }
  };

  const removeRoot = async (rootId: string) => {
    await run(async () => {
      await props.onDetach(rootId);
      setConfirmRemoveId(null);
    });
  };

  const setPrimary = async (rootId: string) => {
    await run(() => props.onSetPrimary(rootId));
  };

  const renameLabel = async (rootId: string, label: string) => {
    const next = label.trim();
    if (!next) return;
    await run(() => props.onRenameLabel(rootId, next));
  };

  return (
    <Show when={props.open}>
      <div
        class="workspace-folders-backdrop"
        data-testid="workspace-folders-backdrop"
        onClick={() => props.onClose()}
      />
      <Scrollport
        class="workspace-folders-panel"
        contentClass="workspace-folders-panel__content"
        data-testid="workspace-folders-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="workspace-folders-title"
        ref={setPanelEl}
      >
        <header class="workspace-folders-panel__header" {...chromeProps()}>
          <h2 id="workspace-folders-title">Folders</h2>
          <button
            type="button"
            class="workspace-folders-panel__close"
            onClick={() => props.onClose()}
            aria-label="Close folders panel"
          >
            ×
          </button>
        </header>

        <Show when={error()}>
          <p class="workspace-folders-panel__error" role="alert">
            {error()}
          </p>
        </Show>

        <ul class="workspace-folders-panel__list">
          <For each={props.roots}>
            {(root) => (
              <li
                class="workspace-folders-panel__row"
                data-testid={`workspace-folder-row-${root.id}`}
                onContextMenu={(e) => openMenu(e, root)}
              >
                <div class="workspace-folders-panel__path">
                  <Show
                    when={renamingRootId() !== root.id}
                    fallback={
                      <InlineRenameInput
                        class="workspace-folders-panel__rename-input"
                        testId="project-root-rename-input"
                        initialValue={root.label}
                        maxLength={ROOT_LABEL_MAX}
                        ariaLabel="Rename folder label"
                        onCommit={(next) => {
                          setRenamingRootId(null);
                          void renameLabel(root.id, next);
                        }}
                        onCancel={() => setRenamingRootId(null)}
                      />
                    }
                  >
                    <button
                      type="button"
                      class="workspace-folders-panel__label"
                      data-testid={`project-root-rename-trigger-${root.id}`}
                      aria-label="Rename folder label"
                      data-tip="Rename"
                      disabled={busy()}
                      onClick={() => setRenamingRootId(root.id)}
                    >
                      {root.label}
                    </button>
                  </Show>
                  <span class="workspace-folders-panel__full">{root.path}</span>
                  <Show when={root.is_primary}>
                    <span class="workspace-folders-panel__badge">Main folder</span>
                  </Show>
                </div>
                <div class="workspace-folders-panel__actions">
                  <Show when={!root.is_primary}>
                    <DenButton
                      variant="secondary"
                      compact
                      disabled={busy()}
                      data-testid={`workspace-folder-set-main-${root.id}`}
                      onClick={() => void setPrimary(root.id)}
                    >
                      Set as main folder
                    </DenButton>
                  </Show>
                  <Show
                    when={confirmRemoveId() === root.id}
                    fallback={
                      <DenButton
                        variant="danger"
                        compact
                        disabled={busy()}
                        data-testid={`workspace-folder-remove-${root.id}`}
                        onClick={() => setConfirmRemoveId(root.id)}
                      >
                        Remove
                      </DenButton>
                    }
                  >
                    <span class="workspace-folders-panel__confirm-text">
                      Remove this folder from the project?
                    </span>
                    <DenButton
                      variant="danger"
                      compact
                      disabled={busy()}
                      data-testid={`workspace-folder-remove-confirm-${root.id}`}
                      onClick={() => void removeRoot(root.id)}
                    >
                      Confirm remove
                    </DenButton>
                    <DenButton
                      variant="ghost"
                      disabled={busy()}
                      onClick={() => setConfirmRemoveId(null)}
                    >
                      Cancel
                    </DenButton>
                  </Show>
                </div>
              </li>
            )}
          </For>
        </ul>

        <footer class="workspace-folders-panel__footer">
          <DenButton
            variant="primary"
            compact
            disabled={busy() || !props.projectId}
            data-testid="workspace-folder-add"
            onClick={() => void addFolder()}
          >
            Add folder…
          </DenButton>
        </footer>
      </Scrollport>
      <Show when={menu()} keyed>
        {(m) => (
          <ContextMenu
            anchor={m.anchor}
            onDismiss={() => setMenu(null)}
            items={[
              contextAction("rename", {
                testId: `project-root-menu-rename-${m.root.id}`,
                onSelect: () => {
                  setMenu(null);
                  setRenamingRootId(m.root.id);
                },
              }),
              ...projectPathMenuItems({
                absolutePath: m.root.path,
                projectId: props.projectId,
                entryKind: "folder",
                rootRefs: props.roots.map((r) => ({
                  id: r.id,
                  path: r.path,
                  is_primary: r.is_primary,
                })),
              }),
              contextAction("removeFolderFromProject", {

                testId: `project-root-menu-remove-${m.root.id}`,
                onSelect: () => setConfirmRemoveId(m.root.id),
              }),
            ]}
          />
        )}
      </Show>
    </Show>
  );
}
