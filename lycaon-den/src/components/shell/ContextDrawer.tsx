import { createContext, Show, useContext, type JSX } from "solid-js";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ChromeCloseButton } from "./ChromeCloseButton.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";

const ContextDrawerHostContext = createContext(false);

export function ContextDrawerHost(props: { children: JSX.Element }) {
  return (
    <ContextDrawerHostContext.Provider value>
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
  children: JSX.Element;
};

export function ContextDrawer(props: Props) {
  const hosted = useContext(ContextDrawerHostContext);
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
        <h2 id={props.titleId}>{props.title}</h2>
        <ChromeCloseButton
          class="den-context-drawer-close den-inset-icon-btn"
          label={props.closeLabel}
          onClick={() => props.onClose()}
        />
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

  return <Show when={hosted && props.open}>{drawer()}</Show>;
}
