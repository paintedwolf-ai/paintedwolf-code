import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { For, Show, createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import type { ProjectSummary } from "../../project/project-summary.ts";
import {
  LAUNCHER_RENDER_CAP,
  filterProjects,
  nextIndex,
} from "../../project/project-launcher-model.ts";
import {
  attachDispatcher,
  registerCommandHandler,
} from "../../shortcuts/dispatcher.ts";
import {
  createOverlayScopeFocusTrap,
  shellChromeInertTargets,
} from "../../platform/interaction/modal-focus-trap.ts";

export type ProjectLauncherProps = {
  open: boolean;
  summaries: ProjectSummary[];
  activeProjectId?: string | null;
  onSwitch: (projectId: string) => void;
  onNewProject: () => void;
  onManageAll: () => void;
  onClose: () => void;
};

export function ProjectLauncher(props: ProjectLauncherProps) {
  const [query, setQuery] = createSignal("");
  const matched = createMemo(() => filterProjects(props.summaries, query()));
  const filtered = createMemo(() => matched().slice(0, LAUNCHER_RENDER_CAP));
  const overflowCount = () => Math.max(0, matched().length - LAUNCHER_RENDER_CAP);
  const [activeIndex, setActiveIndex] = createSignal(0);
  let dialogEl: HTMLDivElement | undefined;

  createOverlayScopeFocusTrap(
    () => props.open,
    () => dialogEl,
    { inertTarget: () => shellChromeInertTargets() },
  );

  const submitActive = () => {
    const row = filtered()[activeIndex()];
    if (!row) return;
    props.onSwitch(row.id);
  };

  createEffect(() => {
    if (!props.open) {
      return;
    }
    const detachUp = registerCommandHandler("list.up", () => {
      setActiveIndex((i) => nextIndex(i, filtered().length, -1));
    });
    const detachDown = registerCommandHandler("list.down", () => {
      setActiveIndex((i) => nextIndex(i, filtered().length, 1));
    });
    const detachFirst = registerCommandHandler("list.first", () => {
      if (filtered().length > 0) setActiveIndex(0);
    });
    const detachLast = registerCommandHandler("list.last", () => {
      const len = filtered().length;
      if (len > 0) setActiveIndex(len - 1);
    });
    const detachConfirm = registerCommandHandler("list.confirm", () => {
      submitActive();
    });
    const detachListener = attachDispatcher();
    onCleanup(() => {
      detachUp();
      detachDown();
      detachFirst();
      detachLast();
      detachConfirm();
      detachListener();
    });
  });

  return (
    <Show when={props.open}>
      <div class="den-dialog-backdrop" onClick={() => props.onClose()}>
        <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
        <div
          ref={dialogEl}
          class="den-launcher"
          data-testid="project-launcher"
          role="dialog"
          aria-modal="true"
          aria-label="Switch project"
          onClick={(e) => e.stopPropagation()}
        >
          <div class="den-launcher__search">
            <ThemeIcon slot="search" size={17} />
            <input
              autofocus
              class="den-launcher__input"
              aria-label="Search projects"
              value={query()}
              placeholder="Search projects…"
              onInput={(e) => {
                setQuery(e.currentTarget.value);
                setActiveIndex(0);
              }}
            />
          </div>

          <Scrollport class="den-launcher__list" contentAs="ul" contentClass="den-launcher__rows">
            <For each={filtered()}>
              {(summary, idx) => (
                <li>
                  <button
                    type="button"
                    class="den-launcher__row"
                    data-testid={`project-launcher-row-${summary.id}`}
                    classList={{ "den-launcher__row--active": idx() === activeIndex() }}
                    onClick={() => props.onSwitch(summary.id)}
                  >
                    <span class="den-launcher__glyph" aria-hidden="true">
                      {summary.displayName.charAt(0).toUpperCase() || "?"}
                    </span>
                    <span class="den-launcher__name">{summary.displayName}</span>
                    <span class="den-launcher__meta">{summary.folderLabel}</span>
                    <Show
                      when={summary.id === props.activeProjectId}
                      fallback={<span class="den-launcher__count">{summary.chatCountLabel}</span>}
                    >
                      <ThemeIcon
                        slot="check"
                        class="den-launcher__check"
                        size={15}
                      />
                    </Show>
                  </button>
                </li>
              )}
            </For>
            <Show when={overflowCount() > 0}>
              <li
                class="den-launcher__empty"
                data-testid="project-launcher-overflow"
              >
                {overflowCount()} more — keep typing to narrow
              </li>
            </Show>
            <Show when={filtered().length === 0}>
              <li class="den-launcher__empty">No projects match “{query()}”.</li>
            </Show>
          </Scrollport>

          <div class="den-launcher__footer">
            <button
              type="button"
              class="den-launcher__action den-launcher__action--primary"
              data-testid="project-launcher-new"
              onClick={() => props.onNewProject()}
            >
              <ThemeIcon slot="plus" size={15} />
              New project
            </button>
            <span class="den-launcher__footer-spacer" />
            <button
              type="button"
              class="den-launcher__action"
              data-testid="project-launcher-manage-all"
              onClick={() => props.onManageAll()}
            >
              Manage all projects…
            </button>
          </div>
        </div>
      </div>
    </Show>
  );
}
