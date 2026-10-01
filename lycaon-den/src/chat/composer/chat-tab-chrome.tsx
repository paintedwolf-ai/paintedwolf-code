import {
  createContext,
  createEffect,
  createSignal,
  useContext,
  type Accessor,
  type ParentProps,
} from "solid-js";
import { type TabId } from "../../components/chatview/tabs.ts";
import { focusRegionContainsActive } from "../../shortcuts/focus-region.ts";

export type ChatTabChromeValue = {
  openTab: Accessor<TabId | null>;
  panelRetracted: Accessor<boolean>;
  tabRefocus: Accessor<number>;
  selectTab: (next: TabId | null) => void;
  setPanelRetracted: (retracted: boolean) => void;
  syncPanelHeight: (heightPx: number) => void;
  panelHeightPx: Accessor<number>;
};

const ChatTabChromeContext = createContext<ChatTabChromeValue>();

export function useChatTabChrome(): ChatTabChromeValue {
  const ctx = useContext(ChatTabChromeContext);
  if (!ctx) {
    throw new Error("ChatTabChromeProvider is required");
  }
  return ctx;
}

type ProviderProps = ParentProps & {
  sessionKey: string;
};

export function ChatTabChromeProvider(props: ProviderProps) {
  const [openTab, setOpenTab] = createSignal<TabId | null>(null);
  const [panelRetracted, setPanelRetractedSignal] = createSignal(false);
  const [tabRefocus, setTabRefocus] = createSignal(0);
  const [panelHeightPx, setPanelHeightPx] = createSignal(0);

  createEffect(() => {
    props.sessionKey;
    setOpenTab(null);
    setPanelRetractedSignal(false);
    setPanelHeightPx(0);
  });

  const setPanelRetracted = (retracted: boolean) => {
    setPanelRetractedSignal(retracted);
  };

  const selectTab = (next: TabId | null) => {
    setPanelRetractedSignal(false);
    setOpenTab(next);
    // Active text regions keep their caret.
    if (
      focusRegionContainsActive("composer") ||
      focusRegionContainsActive("find")
    ) {
      return;
    }
    setTabRefocus((n) => n + 1);
  };

  const value: ChatTabChromeValue = {
    openTab,
    panelRetracted,
    tabRefocus,
    selectTab,
    setPanelRetracted,
    syncPanelHeight: setPanelHeightPx,
    panelHeightPx,
  };

  return (
    <ChatTabChromeContext.Provider value={value}>
      {props.children}
    </ChatTabChromeContext.Provider>
  );
}
