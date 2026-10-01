import { createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import type { NavigationReference, NavigationTarget } from "../../api/types.ts";
import { openSourceLocation, type SourceNavigationAction } from "../../platform/navigation/open-source.ts";
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
import { createPresentationIntent, createPresentationWaiting } from "../../ui/presentation.ts";
import type { ProseNavigationIndex } from "./prose-path-opens.ts";

type ReferenceSelection = {
  action: SourceNavigationAction;
  anchor: Element;
  referenceId: string;
  reference?: NavigationReference;
  error?: string;
  busy: boolean;
};

/** Resolves the host reference before applying either navigation action. */
export function createReferenceNavigation(props: {
  scope: () => unknown;
  navigation: () => ProseNavigationIndex | undefined;
  resolve: (referenceId: string, candidateIndex?: number) => Promise<NavigationReference | undefined>;
}) {
  const [chooser, setChooser] = createSignal<ReferenceSelection>();
  const selection = createPresentationIntent();
  const waiting = createPresentationWaiting(() => chooser()?.busy ?? false);
  const displayedChooser = createMemo<ReferenceSelection | undefined>((previous) => {
    const state = chooser();
    return state && (previous || !state.busy || waiting()) ? state : undefined;
  });
  const dismiss = () => { selection.cancel(); setChooser(undefined); };
  onCleanup(selection.dispose);
  createEffect(() => { props.scope(); dismiss(); });

  const resolveAndApply = async (state: ReferenceSelection, candidateIndex?: number) => {
    const local = selection.begin();
    const navigation = beginSourceNavigation();
    let dispatched = false;
    setChooser({ ...state, busy: true, error: undefined });
    try {
      const ref = await props.resolve(state.referenceId, candidateIndex);
      if (!local.current()) return;
      if (!navigation.current()) { setChooser(undefined); return; }
      if (ref?.status !== "resolved" || !ref.root_id || !ref.entry_kind) {
        setChooser({ ...state, reference: ref, busy: false });
        return;
      }
      dispatched = true;
      const result = await openSourceLocation({
        projectId: ref.project_id, rootId: ref.root_id, jobId: ref.worker_id,
        path: ref.path, entryKind: ref.entry_kind, line: ref.line,
        endLine: ref.end_line,
        intent: "permanent", action: state.action,
      });
      if (result.status === "rejected") throw new Error(result.reason);
      if (result.status === "noop") throw new Error("This location is unavailable in the current workspace.");
      if (local.current()) setChooser(undefined);
    } catch (cause) {
      if (!local.current()) return;
      if (!dispatched && !navigation.current()) { setChooser(undefined); return; }
      setChooser({ ...state, busy: false, error: cause instanceof Error ? cause.message : "File navigation failed" });
    }
  };

  return {
    displayedChooser,
    dismiss,
    async activate(anchor: Element, referenceId: string, action: SourceNavigationAction) {
      const reference = props.navigation()?.references.get(referenceId);
      const state = { action, anchor, referenceId, reference, busy: false };
      if (reference?.status === "ambiguous") {
        selection.cancel();
        beginSourceNavigation();
        setChooser(state);
        return;
      }
      await resolveAndApply(state);
    },
    async choose(target: NavigationTarget) {
      const state = chooser();
      if (!state || state.busy) return;
      const index = state.reference?.candidates?.indexOf(target) ?? -1;
      if (index < 0) return;
      await resolveAndApply(state, index);
    },
  };
}
