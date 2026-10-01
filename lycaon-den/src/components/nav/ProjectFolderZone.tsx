import { contextAction } from "../context-actions.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { For, Show, createSignal } from "solid-js";
import type { ProjectRoot } from "../../api/types.ts";
import { ContextMenu, type ContextMenuAnchor } from "../ContextMenu.tsx";
import { projectPathMenuItems } from "../project-path-menu-items.ts";

type Props = {
  roots: ProjectRoot[];
  projectId: string;
  isDraft?: boolean;
  promotePending?: boolean;
  promotionPhase?: string;
  promotionError?: string;
  onSaveDraft?: () => void;
  onRetryPromotion?: () => void;
  onCancelPromotion?: () => void;
  onAddFolder: () => void;
  onDetach: (rootId: string) => void;
};

function PromotionStatus(props: Props) {
  return (
    <Show when={props.promotePending}>
      <p
        class="project-folders__hint project-folders__hint--pending"
        data-testid="project-folder-promote-pending"
      >
        {props.promotionError || "Saving to folder…"}
      </p>
      <Show when={props.promotionError}>
        <div class="project-folders__promotion-actions">
          <button type="button" onClick={() => props.onRetryPromotion?.()}>
            Retry
          </button>
          <Show when={props.promotionPhase !== "committed"}>
            <button type="button" onClick={() => props.onCancelPromotion?.()}>
              Cancel
            </button>
          </Show>
        </div>
      </Show>
    </Show>
  );
}

const PILE_LIMIT = 3;

export function ProjectFolderZone(props: Props) {
  const [menu, setMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    root: ProjectRoot;
  } | null>(null);
  const [expanded, setExpanded] = createSignal(false);

  const ordered = () => {
    const primary = props.roots.filter((r) => r.is_primary);
    return [...primary, ...props.roots.filter((r) => !r.is_primary)];
  };
  const overflow = () => Math.max(0, props.roots.length - PILE_LIMIT);
  const summaryLabel = () => {
    const lead = ordered()[0];
    return lead?.label ?? "";
  };
  const expandLabel = () => {
    const n = props.roots.length;
    const noun = n === 1 ? "folder" : "folders";
    return expanded() ? `Hide ${n} ${noun}` : `Show all ${n} ${noun}`;
  };

  const openMenu = (e: MouseEvent, root: ProjectRoot) => {
    e.preventDefault();
    e.stopPropagation();
    setMenu({ anchor: { x: e.clientX, y: e.clientY }, root });
  };

  return (
    <Show
      when={!props.isDraft}
      fallback={
        <div
          class="project-folders project-folders--empty project-folders--draft"
          data-testid="project-folder-zone"
        >
          <PromotionStatus {...props} />
          <Show when={!props.promotePending}>
            <button
              type="button"
              class="project-folders__add-primary"
              data-testid="project-folder-save-draft"
              onClick={() => props.onSaveDraft?.()}
            >
              <FolderPlusIcon />
              Save to folder…
            </button>
            <p class="project-folders__hint">
              Choose where to keep this project on disk.
            </p>
          </Show>
        </div>
      }
    >
      <>
        <PromotionStatus {...props} />
        <Show
          when={props.roots.length > 0}
          fallback={
          <div
            class="project-folders project-folders--empty"
            data-testid="project-folder-zone"
          >
            <button
              type="button"
              class="project-folders__add-primary"
              data-testid="project-folder-add"
              onClick={() => props.onAddFolder()}
            >
              <FolderPlusIcon />
              Add folder
            </button>
            <p class="project-folders__hint">
              Point Painted Wolf at your code so it can read and edit files.
            </p>
          </div>
        }
        >
        <div class="project-folders" data-testid="project-folder-zone">
          <div class="project-folders__summary-row">
            <button
              type="button"
              class="project-folders__summary"
              data-testid="project-folder-summary"
              aria-expanded={expanded()}
              aria-label={expandLabel()}
              onClick={() => setExpanded((v) => !v)}
              onContextMenu={(e) => {
                const lead = ordered()[0];
                if (lead) openMenu(e, lead);
              }}
            >
              <span class="project-folders__pile" aria-hidden="true">
                <For each={ordered().slice(0, PILE_LIMIT)}>
                  {(root) => (
                    <span class="project-folders__tile">
                      {root.label.charAt(0)}
                    </span>
                  )}
                </For>
                <Show when={overflow() > 0}>
                  <span
                    class="project-folders__tile project-folders__tile--more"
                    data-testid="project-folder-overflow"
                  >
                    +{overflow()}
                  </span>
                </Show>
              </span>
              <span class="project-folders__path" data-tip={ordered()[0]?.path}>
                {summaryLabel()}
              </span>
            </button>
            <button
              type="button"
              class="project-folders__add-icon den-icon-target den-inset-icon-btn"
              data-testid="project-folder-add"
              aria-label="Add folder"
              data-tip="Add folder"
              onClick={() => props.onAddFolder()}
            >
              <PlusIcon />
            </button>
          </div>
          <ul class="project-folders__list" hidden={!expanded()}>
            <For each={ordered()}>
              {(root) => {
                return (
                  <li
                    class="project-folders__row den-list-row--nav den-list-row"
                    data-testid={`project-folder-row-${root.id}`}
                    onContextMenu={(e) => openMenu(e, root)}
                  >
                    <span class="project-folders__icon" aria-hidden="true">
                      <FolderIcon />
                    </span>
                    <span
                      class="project-folders__path"
                      data-tip={root.path}
                      data-testid={`project-folder-label-${root.id}`}
                    >
                      {root.label}
                    </span>
                    <div class="den-list-row__actions">
                      <button
                        type="button"
                        class="den-row-action den-row-action--danger den-inset-icon-btn"
                        aria-label={`Remove ${root.path} from project`}
                        data-tip="Remove folder from project"
                        data-testid={`project-folder-remove-${root.id}`}
                        onClick={() => props.onDetach(root.id)}
                      >
                        <ThemeIcon slot="dismiss" size={14} />
                      </button>
                    </div>
                  </li>
                );
              }}
            </For>
          </ul>
        </div>
      </Show>
      <Show when={menu()} keyed>
        {(m) => (
          <ContextMenu
            anchor={m.anchor}
            onDismiss={() => setMenu(null)}
            items={[
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

                testId: `project-folder-menu-remove-${m.root.id}`,
                onSelect: () => props.onDetach(m.root.id),
              }),
            ]}
          />
        )}
        </Show>
      </>
    </Show>
  );
}

function FolderIcon() {
  return (
    <ThemeIcon slot="folder" size={14} />
  );
}

function FolderPlusIcon() {
  return (
    <ThemeIcon slot="new-folder" size={16} />
  );
}

function PlusIcon() {
  return (
    <ThemeIcon slot="plus" size={12} />
  );
}
