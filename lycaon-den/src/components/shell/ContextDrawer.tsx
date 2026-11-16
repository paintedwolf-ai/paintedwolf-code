import { createContext, For, Show, useContext, type JSX } from "solid-js";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ChromeCloseButton } from "./ChromeCloseButton.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";
import { WindowControls, type WindowEdgeClaim } from "./WindowControls.tsx";

type DrawerHost = {
  /** Window edges whose drawn controls an open drawer carries in its header. */
  windowEdges: () => readonly WindowEdgeClaim[];
};

const ContextDrawerHostContext = createContext<DrawerHost | null>(null);

export function ContextDrawerHost(props: {
  windowEdges?: readonly WindowEdgeClaim[];
  children: JSX.Element;
}) {
  return (
    <ContextDrawerHostContext.Provider value={{ windowEdges: () => props.windowEdges ?? [] }}>
      {props.children}
    </ContextDrawerHostContext.Provider>
  );
}

type Props = {
  open: boolean;
  testId: string;
  titleId: string;
  title: JSX.Element;
  closeLabel: string;
  onClose: () => void;
  scrollTestId?: string;
  closeTestId?: string;
  children: JSX.Element;
};

export function ContextDrawer(props: Props) {
  const host = useContext(ContextDrawerHostContext);
  const windowControls = (side: "left" | "right") => (
    <For each={host?.windowEdges().filter((claim) => claim.side === side)}>
      {(claim) => <WindowControls {...claim} />}
    </For>
  );
  const drawer = () => (
    <aside
      class="den-context-drawer"
      data-testid={props.testId}
      role="dialog"
      aria-labelledby={props.titleId}
      tabIndex={-1}
      ref={(el) => {
        queueMicrotask(() => el.focus());
      }}
    >
      <header class="den-context-drawer-header" {...chromeProps()}>
        {/* The drawer covers the stage titlebar drag surface. */}
        <ChromeDragSurface class="den-context-drawer__chrome-drag" />
        {windowControls("left")}
        <h2 id={props.titleId}>{props.title}</h2>
        <ChromeCloseButton
          class="den-context-drawer-close den-inset-icon-btn"
          label={props.closeLabel}
          testId={props.closeTestId}
          onClick={() => props.onClose()}
        />
        {windowControls("right")}
      </header>
      <Scrollport
        class="den-context-drawer-scroll"
        contentAs="section"
        eager
        viewport={{ "data-testid": props.scrollTestId }}
      >
        {props.children}
      </Scrollport>
    </aside>
  );

  return <Show when={host != null && props.open}>{drawer()}</Show>;
}
