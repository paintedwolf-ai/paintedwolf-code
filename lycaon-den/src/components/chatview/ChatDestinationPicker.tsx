import { sessionTitle } from "../../chat/session/session-title.ts";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import {
  For,
  Show,
  createEffect,
  createSignal,
  onCleanup,
} from "solid-js";
import type { Project, SessionSummary } from "../../api/types.ts";
import {
  pendingChatDestinationRequest,
  openChatDestination,
  settleChatDestination,
} from "../../chat/composer/chat-destination.ts";
import {
  createSessionForProject,
  recordCreatedSessionInRecents,
} from "../../chat/session/session-lifecycle.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import type { ProjectSessionsStore } from "../../store/project-sessions-store.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

type Props = {
  projectSessions: ProjectSessionsStore;
  recents: RecentsStore;
};

function chatTitle(session: SessionSummary): string {
  return sessionTitle(session.title);
}

function projectTitle(projects: readonly Project[], projectId: string): string {
  return projects.find((project) => project.id === projectId)?.name?.trim() || "Project";
}

/** One destination chooser shared by every project/app-level Add-to-chat action. */
export function ChatDestinationPicker(props: Props) {
  const request = pendingChatDestinationRequest;
  const [projects, setProjects] = createSignal<Project[]>([]);
  const [projectId, setProjectId] = createSignal("");
  const [sessions, setSessions] = createSignal<SessionSummary[]>([]);
  const [loadingProjects, setLoadingProjects] = createSignal(false);
  const [loadingSessions, setLoadingSessions] = createSignal(false);
  const [creating, setCreating] = createSignal(false);
  const [error, setError] = createSignal<string | null>(null);
  let dialogEl: HTMLDivElement | undefined;
  let generation = 0;

  // A window stop tears this global host down. Resolve the caller as cancelled
  // rather than leaving its staged operation waiting behind a dead window.
  onCleanup(() => settleChatDestination(null));

  createModalFocusTrap(
    () => request() != null,
    () => dialogEl,
    { onEscape: () => settleChatDestination(null) },
  );

  const loadSessions = async (nextProjectId: string, gen: number) => {
    const client = getLycaonClient();
    if (!client || !nextProjectId) return;
    setLoadingSessions(true);
    setSessions([]);
    setError(null);
    try {
      const page = await client.listProjectSessions(nextProjectId, { limit: 200 });
      if (gen !== generation) return;
      setSessions(page.sessions);
    } catch {
      if (gen === generation) setError("Could not load chats for this project.");
    } finally {
      if (gen === generation) setLoadingSessions(false);
    }
  };

  createEffect(() => {
    const current = request();
    if (!current) return;
    const gen = ++generation;
    setProjects([]);
    setSessions([]);
    setError(null);
    const initialProjectId =
      current.projectId ?? current.suggested?.projectId ?? "";
    setProjectId(initialProjectId);
    setLoadingProjects(true);
    const client = getLycaonClient();
    if (!client) {
      setLoadingProjects(false);
      setError("Connect to the backend to choose a chat.");
      return;
    }
    void client
      .listProjects()
      .then((listed) => {
        if (gen !== generation) return;
        setProjects(listed);
        const selected = initialProjectId || listed[0]?.id || "";
        setProjectId(selected);
        if (selected) void loadSessions(selected, gen);
      })
      .catch(() => {
        if (gen === generation) setError("Could not load projects.");
      })
      .finally(() => {
        if (gen === generation) setLoadingProjects(false);
      });
  });

  const changeProject = (nextProjectId: string) => {
    const gen = ++generation;
    setProjectId(nextProjectId);
    void loadSessions(nextProjectId, gen);
  };

  const createChat = async () => {
    const client = getLycaonClient();
    const selectedProjectId = projectId();
    if (!client || !selectedProjectId || creating()) return;
    setCreating(true);
    setError(null);
    try {
      const session = await createSessionForProject(client, selectedProjectId);
      const destination = {
        projectId: session.project_id?.trim() || selectedProjectId,
        sessionId: session.id,
      };
      await recordCreatedSessionInRecents(props.recents, destination);
      await props.projectSessions.refresh();
      await openChatDestination(destination);
      settleChatDestination(destination);
    } catch {
      setError("Could not create a chat in this project.");
    } finally {
      setCreating(false);
    }
  };

  return (
    <Show when={request()} keyed>
      {(current) => (
        <div
          class="den-dialog-backdrop"
          data-testid="chat-destination-backdrop"
          onClick={(event) => {
            if (event.target === event.currentTarget && !creating()) {
              settleChatDestination(null);
            }
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={dialogEl}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="chat-destination-title"
            data-testid="chat-destination-picker"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="chat-destination-title">Choose a chat</h2>
              <button
                type="button"
                class="den-dialog__close"
                aria-label="Cancel"
                disabled={creating()}
                onClick={() => settleChatDestination(null)}
              >
                ×
              </button>
            </header>
            <p class="den-dialog__hint">The content stays staged until you send it.</p>
            <Show when={!current.projectId && projects().length > 1}>
              <label class="den-dialog__label" for="chat-destination-project">
                Project
              </label>
              <DenSelect
                id="chat-destination-project"
                aria-label="Project"
                class="chat-destination-project"
                data-testid="chat-destination-project"
                value={projectId()}
                options={projects().map((project) => ({
                  value: project.id,
                  label: project.name?.trim() || "Untitled project",
                }))}
                onValueChange={changeProject}
              />
            </Show>
            <Show when={projectId()}>
              {(selectedProjectId) => (
                <p class="den-dialog__label">
                  {projectTitle(projects(), selectedProjectId())}
                </p>
              )}
            </Show>
            <Show when={error()}>
              {(message) => (
                <p class="den-dialog__error" role="alert" data-testid="chat-destination-error">
                  {message()}
                </p>
              )}
            </Show>
            <Show when={loadingProjects() || loadingSessions()}>
              <p class="den-dialog__hint" data-testid="chat-destination-loading">
                Loading chats…
              </p>
            </Show>
            <Show when={!loadingProjects() && !loadingSessions()}>
              <Scrollport
                class="den-dialog__list"
                contentAs="ul"
                contentClass="den-dialog__list-content"
                data-testid="chat-destination-list"
              >
                <For each={sessions()}>
                  {(session) => {
                    const suggested =
                      current.suggested?.projectId === session.project_id &&
                      current.suggested.sessionId === session.id;
                    return (
                      <li>
                        <button
                          type="button"
                          class="den-dialog__row"
                          classList={{ "chat-destination-row--suggested": suggested }}
                          data-testid="chat-destination-row"
                          data-session-id={session.id}
                          onClick={() =>
                            settleChatDestination({
                              projectId: session.project_id,
                              sessionId: session.id,
                            })
                          }
                        >
                          <span class="den-dialog__row-title">{chatTitle(session)}</span>
                          <span class="den-dialog__row-sub">
                            {suggested ? "Current chat · " : ""}
                            {session.message_count} {session.message_count === 1 ? "message" : "messages"}
                          </span>
                        </button>
                      </li>
                    );
                  }}
                </For>
              </Scrollport>
            </Show>
            <footer class="den-dialog__footer">
              <DenButton
                variant="secondary"
                class="den-dialog__add"
                data-testid="chat-destination-new"
                disabled={!projectId() || creating()}
                onClick={() => void createChat()}
              >
                {creating() ? "Creating…" : "New chat"}
              </DenButton>
            </footer>
          </div>
        </div>
      )}
    </Show>
  );
}
