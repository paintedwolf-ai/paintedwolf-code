import { Show, createMemo, createSignal, onCleanup, type Accessor, type ComponentProps } from "solid-js";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { createPreparation } from "../../ui/presentation.ts";
import { PresentationProvider, usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { FilesTreeEntrySelection } from "../tree/files-tree-context.ts";
import { StepBar } from "./StepBar.tsx";
import { createWalkPresentation } from "./walk-presentation.ts";
import { currentWalkStep, isWalking, leaveWalk, subscribeWalk, walkState } from "./walk-store.ts";

export function createFilesWorkspaceWalk(options: {
  projectId: Accessor<string>;
  paneMode: Accessor<string>;
  selectedEntry: Accessor<FilesTreeEntrySelection | null>;
}) {
  const [walkTick, setWalkTick] = createSignal(0);
  onCleanup(subscribeWalk((id) => {
    if (id === options.projectId().trim()) setWalkTick((tick) => tick + 1);
  }));
  const walking = createMemo(() => {
    void walkTick();
    return isWalking(options.projectId());
  });
  const walkEditorPreparation = createPreparation();
  const walkChrome = createWalkPresentation({
    active: walking,
    loading: () => { void walkTick(); return walkState(options.projectId()).status === "loading"; },
    ready: () => {
      void walkTick();
      return !currentWalkStep(options.projectId()) || walkEditorPreparation.ready();
    },
  });
  usePresentationParticipant("files-editors", walkEditorPreparation.ready, walkEditorPreparation.notice);
  // Effect steps show a file version; group steps show a summary page.
  const walkPresentation = createMemo(() => {
    void walkTick();
    const record = walkState(options.projectId());
    const step = currentWalkStep(options.projectId());
    if (!record.active || !step) return null;
    if (step.kind !== "effect") return { step, comparison: null, selectionRevision: record.selectionRevision };
    if (!record.comparison) return null;
    return { step, comparison: record.comparison, selectionRevision: record.selectionRevision };
  }, null, {
    equals: (before, after) => before === after || !!(before && after &&
      before.step.key === after.step.key && before.comparison === after.comparison &&
      before.selectionRevision === after.selectionRevision),
  });
  const walkInitialFile = createMemo(() => {
    if (options.paneMode() !== "files") return null;
    const selection = options.selectedEntry();
    if (selection?.kind !== "file") return null;
    return { rootId: selection.rootId, path: selection.path };
  });
  return { walking, walkEditorPreparation, walkChrome, walkPresentation, walkInitialFile };
}

export function FilesWalkStatus(props: {
  projectId: string;
  walk: Pick<ReturnType<typeof createFilesWorkspaceWalk>, "walking" | "walkChrome">;
  onRevealInTranscript: ComponentProps<typeof StepBar>["onRevealInTranscript"];
}) {
  return (
    <>
      <Show when={props.walk.walking()}>
        <PresentationProvider preparation={props.walk.walkChrome.preparation}>
          <ResidentPresenceProvider presence={props.walk.walkChrome.reveal.ready() ? "active" : "pending"}>
            <StepBar
              projectId={props.projectId}
              presentation={props.walk.walkChrome.reveal}
              onRevealInTranscript={props.onRevealInTranscript}
            />
          </ResidentPresenceProvider>
        </PresentationProvider>
      </Show>
      <Show when={props.walk.walkChrome.waiting()}>
        <div class="den-presentation-wait" role="status" data-testid="walk-preparing">
          Preparing walk…
          <DenButton class="pointer-events-auto" variant="link" compact onClick={() => leaveWalk(props.projectId)}>Cancel</DenButton>
        </div>
      </Show>
    </>
  );
}
