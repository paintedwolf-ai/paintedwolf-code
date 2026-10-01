import { createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import type { AppStore } from "../../store/app-state-model.ts";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import type { FilesScope } from "../components/files-scope.ts";
import { setSourceRefresh, type SourceRefresh } from "../source/source-refresh.ts";
import { resolveScope, rebindScopeSubject, scopeContentEpoch, scopeMarksEpoch, setScopeResolver, subscribeResolvedScope } from "../tree/scope-resolution.ts";
import { getSidebarScope } from "./review-pane.ts";
import { chatIsTheQuestion, subjectAddress, type ScopeSubject } from "./review-model.ts";

/** Binds one Files stage to its project's resolved review comparison and the chat it follows. */
export function createFilesReviewScope(options: Pick<FilesScope, "projectId" | "client"> & {
  appStore: AppStore;
  /** The selected chat when it belongs to this project. */
  chatSessionId: () => string | undefined;
  /** Whether the chat's own worktree answers this project's reads. */
  sessionScoped: () => boolean;
  refreshWatchCoverage: () => Promise<unknown>;
}) {
  const { projectId, client, chatSessionId, sessionScoped } = options;
  /** Updates row decorations independently from tree structure. */
  const [scopeMarksVersion, setScopeMarksVersion] = createSignal(scopeMarksEpoch(projectId()));
  /** The resolved comparison behind review targets and tab labels. */
  const [scopeContentVersion, setScopeContentVersion] = createSignal(scopeContentEpoch(projectId()));
  onCleanup(
    subscribeResolvedScope((id) => {
      if (id !== projectId().trim()) return;
      // Untracked resolution preserves the caller's reactive dependencies.
      untrack(() => {
        setScopeMarksVersion(scopeMarksEpoch(id));
        setScopeContentVersion(scopeContentEpoch(id));
      });
    }),
  );

  /** Review comparisons follow the chat selected beside this project. */
  const scopeSubject = createMemo<ScopeSubject | null>(
    () => {
      const sessionId = chatSessionId();
      return sessionId
        ? { sessionId, title: options.appStore.state.currentSession?.title?.trim() ?? "", sessionScoped: sessionScoped() }
        : null;
    },
    null,
    { equals: (a, b) => a?.sessionId === b?.sessionId && a?.title === b?.title && a?.sessionScoped === b?.sessionScoped },
  );
  /** Turn scopes refresh when the selected chat advances. */
  const subjectTurn = () =>
    chatSessionId() ? options.appStore.state.currentSession?.current_turn ?? 0 : 0;
  /** What the host would be asked; unchanged when another chat shares the checkout. */
  const question = createMemo(
    () => ({ subject: scopeSubject(), turn: subjectTurn() }),
    undefined,
    {
      equals: (a, b) => {
        const scope = getSidebarScope(untrack(projectId));
        return subjectAddress(scope, a.subject) === subjectAddress(scope, b.subject) &&
          (!chatIsTheQuestion(scope) || a.turn === b.turn);
      },
    },
  );
  const refreshScopeMarks = async () => {
    await resolveScope(projectId(), client(), untrack(scopeSubject));
  };
  // Source batches can report restored watch coverage or a resync.
  const refreshSourcePresentation = async ({ watchCoverage }: SourceRefresh) => {
    await Promise.all([
      refreshScopeMarks(),
      watchCoverage ? options.refreshWatchCoverage().catch(() => undefined) : Promise.resolve(),
    ]);
  };
  createResidentActivity(() => {
    createEffect(() => {
      void projectId();
      void client();
      void question();
      void refreshScopeMarks();
    });
    createEffect(() => rebindScopeSubject(projectId(), scopeSubject()));
    setScopeResolver(projectId(), () => void refreshScopeMarks());
    onCleanup(() => setScopeResolver(projectId(), null));
    setSourceRefresh(projectId(), refreshSourcePresentation);
    onCleanup(() => setSourceRefresh(projectId(), null));
  });
  return { scopeMarksVersion, scopeContentVersion, refreshScopeMarks };
}
