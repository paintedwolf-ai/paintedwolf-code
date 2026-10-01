import { Show, createSignal, onCleanup } from "solid-js";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { StepBar } from "./StepBar.tsx";
import { WalkTransport } from "./WalkTransport.tsx";
import { createWalkPresentation } from "./walk-presentation.ts";
import { subscribeWalk, walkState } from "./walk-store.ts";

export function WalkChromeFixture(props: { projectId: string }) {
  const [tick, setTick] = createSignal(0);
  onCleanup(subscribeWalk((id) => {
    if (id === props.projectId) setTick((value) => value + 1);
  }));
  const state = () => { void tick(); return walkState(props.projectId); };
  const presentation = createWalkPresentation({
    active: () => state().active,
    loading: () => state().status === "loading",
  });
  return <Show when={state().active}>
    <PresentationProvider preparation={presentation.preparation}>
      <StepBar projectId={props.projectId} presentation={presentation.reveal} />
      <WalkTransport projectId={props.projectId} presentation={presentation.reveal} />
    </PresentationProvider>
  </Show>;
}
