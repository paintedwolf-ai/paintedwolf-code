import { For, Show, type JSX } from "solid-js";
import type { AppNotice } from "../notices/notice-model.ts";
import { noticeActions } from "../notices/notice-actions.ts";
import { NOTICE_LIST_CAP, andNMore } from "../notices/notice-rollup.ts";
import { SystemNudge } from "./SystemNudge.tsx";

type Props = {
  projectId: string;
  projectName: string | undefined;
  notices: readonly AppNotice[];
  onOpenProject: (projectId: string) => void;
  onDismiss: (id: string) => void;
};

/** One attributed project group in the shared notification stack. */
export function ProjectNoticeGroup(props: Props): JSX.Element {
  const shown = () => props.notices.slice(0, NOTICE_LIST_CAP);
  const hidden = () => Math.max(0, props.notices.length - NOTICE_LIST_CAP);
  const name = () => props.projectName?.trim() || undefined;

  return (
    <Show when={props.notices.length > 0}>
      <section
        class="den-project-notices"
        data-testid="project-notices"
        data-project-id={props.projectId}
        aria-label={`${name() ?? "Unavailable project"} notifications`}
      >
        <div class="den-project-notices__scope">
          <span class="den-project-notices__kind">Project</span>
          <Show
            when={name()}
            keyed
            fallback={
              <span class="den-project-notices__unavailable">Unavailable project</span>
            }
          >
            {(projectName) => (
              <button
                type="button"
                class="den-project-notices__link"
                aria-label={`Open project ${projectName}`}
                onClick={() => props.onOpenProject(props.projectId)}
              >
                <span>{projectName}</span>
                <span aria-hidden="true">→</span>
              </button>
            )}
          </Show>
        </div>
        <div class="den-project-notices__cards">
          <For each={shown()}>
            {(notice) => {
              const actions = noticeActions(notice);
              const primary = actions[0];
              const secondary = actions[1];
              const tertiary = actions[2];
              return (
                <SystemNudge
                  testId="project-notice-card"
                  role="status"
                  title={notice.title}
                  description={
                    <>
                      {notice.message}
                      <Show when={notice.suggestedAction} keyed>
                        {(suggestedAction) => (
                          <span class="system-nudge__hint">{suggestedAction}</span>
                        )}
                      </Show>
                    </>
                  }
                  primaryAction={
                    primary
                      ? {
                          label: primary.label,
                          testId: "project-notice-action",
                          onClick: () => {
                            primary.run();
                            props.onDismiss(notice.id);
                          },
                        }
                      : undefined
                  }
                  secondaryAction={
                    secondary
                      ? {
                          label: secondary.label,
                          testId: "project-notice-action-secondary",
                          onClick: () => {
                            secondary.run();
                            props.onDismiss(notice.id);
                          },
                        }
                      : undefined
                  }
                  tertiaryAction={
                    tertiary
                      ? {
                          label: tertiary.label,
                          testId: "project-notice-action-tertiary",
                          onClick: () => {
                            tertiary.run();
                            props.onDismiss(notice.id);
                          },
                        }
                      : undefined
                  }
                  onDismiss={() => props.onDismiss(notice.id)}
                />
              );
            }}
          </For>
          <Show when={hidden() > 0}>
            <p class="den-project-notices__more">{andNMore(hidden())}</p>
          </Show>
        </div>
      </section>
    </Show>
  );
}
