import { createContext, useContext, type JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { createRovingFocus } from "../../platform/interaction/roving-focus.ts";

/** Segment hook a popup trigger renders inside a group; also the nav selector. */
const SEGMENT_SELECTOR = ".den-trigger-group__segment";

const TriggerGroupContext = createContext(false);

/** True where a popup trigger renders as a track segment instead of a plate. */
export function useTriggerGroup(): boolean {
  return useContext(TriggerGroupContext);
}

type Props = {
  /** Names the toolbar for assistive technology. */
  ariaLabel: string;
  children: JSX.Element;
  class?: string;
  testId?: string;
};

/**
 * One recessed track holding a run of labelled popup triggers. Every member renders on every
 * pass and disables when it has nothing to offer, so the track keeps its width and seams.
 * The raise marks only the open popup; the group has no chosen member.
 */
export function DenTriggerGroup(props: Props) {
  let root: HTMLDivElement | undefined;

  createRovingFocus(() => root, {
    items: SEGMENT_SELECTOR,
    keys: {
      ArrowDown: (segment) => {
        if (segment.getAttribute("aria-expanded") !== "true") segment.click();
      },
    },
  });

  return (
    <div
      ref={(element) => {
        root = element;
      }}
      class={cn("den-trigger-group", props.class)}
      role="toolbar"
      aria-label={props.ariaLabel}
      data-testid={props.testId}
    >
      <TriggerGroupContext.Provider value={true}>
        {props.children}
      </TriggerGroupContext.Provider>
    </div>
  );
}
