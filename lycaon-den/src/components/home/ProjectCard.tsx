import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Show, createEffect, createResource, createSignal, onCleanup } from "solid-js";
import type { ProjectSummary } from "../../project/project-summary.ts";
import { ProjectThumbnail } from "./ProjectThumbnail.tsx";
import { productCoverRef, resolveProjectCardThumbSrc } from "../../home/project-cover-src.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { InlineRenameInput } from "../inline-rename/InlineRenameInput.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { artifactWasDeleted, useArtifactDeletion } from "../../chat/visual/artifact-change-store.ts";

type Props = {
  project: ProjectSummary;
  onOpen: (projectId: string) => void;
  onToggleStar: (projectId: string, starred: boolean) => void;
  onRename: (projectId: string, name: string) => void;
  onAttachFolder: (projectId: string) => void;
  onPromote: (projectId: string) => void;
  onDelete: (projectId: string) => void;
};

export function ProjectCard(props: Props) {
  const [menuOpen, setMenuOpen] = createSignal(false);
  const [renaming, setRenaming] = createSignal(false);
  let menuTriggerEl: HTMLButtonElement | undefined;

  const coverKey = () => {
    const ref = productCoverRef(props.project);
    const client = getLycaonClient();
    if (!ref || !client || artifactWasDeleted(ref.artifactId)) return null;
    return { client, ...ref };
  };
  const [coverResource] = createResource(coverKey, async (key) =>
    key.client.getSessionArtifact(key.sessionId, key.artifactId),
  );
  // Reading the accessor rethrows a failed fetch; the guard keeps a missing
  // cover on the generated thumbnail instead of escaping to the stage boundary.
  useArtifactDeletion(() => productCoverRef(props.project)?.artifactId ?? "", () => coverResource.error);
  const coverBlob = () =>
    !artifactWasDeleted(productCoverRef(props.project)?.artifactId ?? "") &&
    coverResource.error === undefined ? coverResource() : undefined;
  const [coverObjectUrl, setCoverObjectUrl] = createSignal<string | null>(null);
  createEffect(() => {
    const b = coverBlob();
    if (!b) {
      setCoverObjectUrl(null);
      return;
    }
    const url = URL.createObjectURL(b);
    setCoverObjectUrl(url);
    onCleanup(() => URL.revokeObjectURL(url));
  });
  const thumbSrc = () => resolveProjectCardThumbSrc(props.project, coverObjectUrl());
  /** Stay blank while a cover may still arrive. */
  const coverPending = () =>
    Boolean(coverKey()) && !coverObjectUrl() && coverResource.loading;

  const startRename = () => {
    setRenaming(true);
    setMenuOpen(false);
  };

  return (
    <div class="project-card" data-testid={`project-card-${props.project.id}`}>
      <button
        type="button"
        class="project-card__thumb-btn"
        aria-label={`Open ${props.project.displayName}`}
        onClick={() => props.onOpen(props.project.id)}
      >
        <ProjectThumbnail
          seed={props.project.id}
          src={thumbSrc()}
          pending={coverPending()}
        />
        <Show when={props.project.isDraft}>
          <span class="project-card__draft-badge" data-testid={`project-card-draft-${props.project.id}`}>
            Draft
          </span>
        </Show>
      </button>
      <div class="project-card__footer">
        <div class="project-card__meta">
          <Show
            when={!renaming()}
            fallback={
              <InlineRenameInput
                class="project-card__rename"
                testId={`project-card-rename-${props.project.id}`}
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
              class="project-card__name"
              onClick={() => props.onOpen(props.project.id)}
            >
              {props.project.displayName}
            </button>
          </Show>
          <span class="project-card__sub">
            {props.project.folderLabel}
            <Show when={props.project.lastActivityLabel !== "new"}>
              {" · "}
              {props.project.lastActivityLabel}
            </Show>
          </span>
        </div>
        <button
          type="button"
          class="project-card__star den-inset-icon-btn"
          classList={{ "project-card__star--on": props.project.starred }}
          aria-label={props.project.starred ? "Unstar project" : "Star project"}
          aria-pressed={props.project.starred}
          data-testid={`project-card-star-${props.project.id}`}
          onClick={() => props.onToggleStar(props.project.id, !props.project.starred)}
        >
          <ThemeIcon slot="starred" size={15} />
        </button>
        <div>
          <button
            type="button"
            class="project-card__menu-btn den-inset-icon-btn"
            aria-label="Project actions"
            aria-haspopup="menu"
            aria-expanded={menuOpen()}
            data-testid={`project-card-menu-${props.project.id}`}
            ref={(element) => {
              menuTriggerEl = element;
            }}
            onClick={() => setMenuOpen((v) => !v)}
          >
            <ThemeIcon slot="more" size={15} />
          </button>
          <Show when={menuOpen()}>
            <AnchoredSurface
              class="project-card__menu"
              role="menu"
              ariaLabel="Project actions"
              anchor={() => menuTriggerEl}
              preferredSide="top"
              align="end"
              onDismiss={() => setMenuOpen(false)}
            >
              <Show when={props.project.isDraft}>
                <button
                  type="button"
                  role="menuitem"
                  data-testid={`project-card-promote-${props.project.id}`}
                  onClick={() => { setMenuOpen(false); props.onPromote(props.project.id); }}
                >
                  Save as a project
                </button>
              </Show>
              <button type="button" role="menuitem" onClick={startRename}>Rename</button>
              <button
                type="button"
                role="menuitem"
                data-testid={`project-card-attach-${props.project.id}`}
                onClick={() => { setMenuOpen(false); props.onAttachFolder(props.project.id); }}
              >
                Attach folder…
              </button>
              <button type="button" role="menuitem" class="project-card__menu-danger" onClick={() => { setMenuOpen(false); props.onDelete(props.project.id); }}>{props.project.isDraft ? "Discard draft" : "Delete"}</button>
            </AnchoredSurface>
          </Show>
        </div>
      </div>
    </div>
  );
}
