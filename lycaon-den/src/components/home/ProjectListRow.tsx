import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Show, createSignal } from "solid-js";
import type { ProjectSummary } from "../../project/project-summary.ts";
import { InlineRenameInput } from "../inline-rename/InlineRenameInput.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";

type Props = {
  project: ProjectSummary;
  selected: boolean;
  onToggleSelect: (projectId: string, selected: boolean) => void;
  onOpen: (projectId: string) => void;
  onToggleStar: (projectId: string, starred: boolean) => void;
  onRename: (projectId: string, name: string) => void;
  onAttachFolder: (projectId: string) => void;
  onPromote: (projectId: string) => void;
  onDelete: (projectId: string) => void;
};

export function ProjectListRow(props: Props) {
  const [menuOpen, setMenuOpen] = createSignal(false);
  const [renaming, setRenaming] = createSignal(false);
  let menuTriggerEl: HTMLButtonElement | undefined;

  const startRename = () => {
    setRenaming(true);
    setMenuOpen(false);
  };

  const initial = () => {
    const ch = props.project.displayName.trim().charAt(0);
    return ch ? ch.toUpperCase() : "?";
  };

  const activity = () =>
    props.project.lastActivityLabel === "new" ? "—" : props.project.lastActivityLabel;

  return (
    <li
      class="project-list-row"
      classList={{ "project-list-row--selected": props.selected }}
      data-testid={`project-list-row-${props.project.id}`}
    >
      <label class="project-list-check">
        <DenCheckboxControl
          checked={props.selected}
          aria-label={`Select ${props.project.displayName}`}
          data-testid={`project-list-select-${props.project.id}`}
          onChange={(e) => props.onToggleSelect(props.project.id, e.currentTarget.checked)}
        />
      </label>
      <span class="project-list-row__glyph" aria-hidden="true">
        {initial()}
      </span>
      <div class="project-list-row__meta">
        <Show
          when={!renaming()}
          fallback={
            <InlineRenameInput
              class="project-list-row__rename"
              testId={`project-list-rename-${props.project.id}`}
              initialValue={props.project.displayName}
              onCommit={(next) => {
                setRenaming(false);
                props.onRename(props.project.id, next);
              }}
              onCancel={() => setRenaming(false)}
            />
          }
        >
          <button
            type="button"
            class="project-list-row__name"
            onClick={() => props.onOpen(props.project.id)}
          >
            {props.project.displayName}
          </button>
        </Show>
        <button
          type="button"
          class="project-list-row__path"
          onClick={() => props.onOpen(props.project.id)}
        >
          {props.project.folderLabel}
        </button>
      </div>
      <span class="project-list-row__activity">{activity()}</span>
      <button
        type="button"
        class="project-list-row__star den-inset-icon-btn"
        classList={{ "project-list-row__star--on": props.project.starred }}
        aria-label={props.project.starred ? "Unstar project" : "Star project"}
        aria-pressed={props.project.starred}
        data-testid={`project-list-star-${props.project.id}`}
        onClick={() => props.onToggleStar(props.project.id, !props.project.starred)}
      >
        <ThemeIcon slot="starred" size={15} />
      </button>
      <div>
        <button
          type="button"
          class="project-list-row__menu-btn den-inset-icon-btn"
          aria-label="Project actions"
          aria-haspopup="menu"
          aria-expanded={menuOpen()}
          data-testid={`project-list-menu-${props.project.id}`}
          ref={(element) => {
            menuTriggerEl = element;
          }}
          onClick={() => setMenuOpen((v) => !v)}
        >
          <ThemeIcon slot="more" size={15} />
        </button>
        <Show when={menuOpen()}>
          <AnchoredSurface
            class="project-list-row__menu"
            role="menu"
            ariaLabel="Project actions"
            anchor={() => menuTriggerEl}
            preferredSide="bottom"
            align="end"
            onDismiss={() => setMenuOpen(false)}
          >
            <Show when={props.project.isDraft}>
              <button
                type="button"
                role="menuitem"
                data-testid={`project-list-promote-${props.project.id}`}
                onClick={() => {
                  setMenuOpen(false);
                  props.onPromote(props.project.id);
                }}
              >
                Save as a project
              </button>
            </Show>
            <button type="button" role="menuitem" onClick={startRename}>
              Rename
            </button>
            <button
              type="button"
              role="menuitem"
              data-testid={`project-list-attach-${props.project.id}`}
              onClick={() => {
                setMenuOpen(false);
                props.onAttachFolder(props.project.id);
              }}
            >
              Attach folder…
            </button>
            <button
              type="button"
              role="menuitem"
              class="project-list-row__menu-danger"
              data-testid={`project-list-delete-${props.project.id}`}
              onClick={() => {
                setMenuOpen(false);
                props.onDelete(props.project.id);
              }}
            >
              {props.project.isDraft ? "Discard draft" : "Delete"}
            </button>
          </AnchoredSurface>
        </Show>
      </div>
    </li>
  );
}
