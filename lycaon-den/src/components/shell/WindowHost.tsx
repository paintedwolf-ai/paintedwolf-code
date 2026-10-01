import type { JSX } from "solid-js";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { tabBarTrailingControls } from "./WindowControls.tsx";

type Props = {
  testId: string;
  children: JSX.Element;
};

/** Full-window surface for the states that replace the shell. Carries window
 *  controls and a drag layer the shell header would otherwise provide. */
export function WindowHost(props: Props) {
  return (
    <Scrollport class="den-window-host" contentClass="den-window-host__surface" data-testid={props.testId}>
      <ChromeDragSurface class="den-window-host__chrome-drag" />
      <div class="den-window-host__controls">{tabBarTrailingControls()}</div>
      {props.children}
    </Scrollport>
  );
}
