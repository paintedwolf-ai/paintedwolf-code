import { For, Show, createMemo, type JSX } from "solid-js";
import type { NoticeIndex } from "../notices/notice-store.ts";
import {
  selectAppNotices,
  selectProjectNoticeGroups,
} from "../notices/notice-select.ts";
import { NoticeRail } from "./NoticeRail.tsx";
import { ProjectNoticeGroup } from "./ProjectNoticeGroup.tsx";

type Props = {
  index: NoticeIndex;
  activeProjectId?: string;
  projectName: (projectId: string) => string | undefined;
  onOpenProject: (projectId: string) => void;
  onDismiss: (id: string) => void;
  onDismissApp: () => void;
  announceLive?: boolean;
};

/** App and project notifications, in the same order on every surface. */
export function NotificationStack(props: Props): JSX.Element {
  const appNotices = createMemo(() => selectAppNotices(props.index));
  const projectGroups = createMemo(() =>
    selectProjectNoticeGroups(props.index, props.activeProjectId),
  );
  const hasNotices = () => appNotices().length > 0 || projectGroups().length > 0;

  return (
    <Show when={hasNotices()}>
      <div class="den-notification-stack" data-testid="notification-stack">
        <Show when={appNotices().length > 0}>
          <NoticeRail
            notices={appNotices()}
            onDismiss={props.onDismiss}
            onDismissAll={props.onDismissApp}
            announceLive={props.announceLive}
          />
        </Show>
        <For each={projectGroups()}>
          {(group) => (
            <ProjectNoticeGroup
              projectId={group.projectId}
              projectName={props.projectName(group.projectId)}
              notices={group.notices}
              onOpenProject={props.onOpenProject}
              onDismiss={props.onDismiss}
            />
          )}
        </For>
      </div>
    </Show>
  );
}
