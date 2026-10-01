// @vitest-environment jsdom
import { createSignal } from "solid-js";
import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import {
  ChatTabChromeProvider,
  useChatTabChrome,
  type ChatTabChromeValue,
} from "./chat-tab-chrome.tsx";

describe("ChatTabChromeProvider", () => {
  it("does not make the session reset reactive to panel retraction", () => {
    const [sessionKey, setSessionKey] = createSignal("session-a");
    let chrome: ChatTabChromeValue | undefined;
    const Probe = () => {
      chrome = useChatTabChrome();
      return null;
    };

    render(() => (
      <ChatTabChromeProvider sessionKey={sessionKey()}>
        <Probe />
      </ChatTabChromeProvider>
    ));

    chrome?.setPanelRetracted(true);
    expect(chrome?.panelRetracted()).toBe(true);

    setSessionKey("session-b");
    expect(chrome?.panelRetracted()).toBe(false);
  });

  it("selects a tab", () => {
    let chrome: ChatTabChromeValue | undefined;
    const Probe = () => {
      chrome = useChatTabChrome();
      return null;
    };
    render(() => (
      <ChatTabChromeProvider sessionKey="session-a">
        <Probe />
      </ChatTabChromeProvider>
    ));
    chrome?.selectTab("git");
    expect(chrome?.openTab()).toBe("git");
  });
});
