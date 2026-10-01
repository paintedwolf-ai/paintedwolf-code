import { Composer } from "../../test/composer-view-fixture.tsx";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";

describe("Composer", () => {

  it("focuses the message field when focusWhen changes", async () => {
    const [sessionId, setSessionId] = createSignal<string | null>(null);
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId={sessionId() ?? undefined}
        focusWhen={sessionId()}
        onSend={vi.fn()}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    expect(document.activeElement).not.toBe(input);

    setSessionId("sess-a");
    await Promise.resolve();
    expect(document.activeElement).toBe(input);

    setSessionId("sess-b");
    await Promise.resolve();
    expect(document.activeElement).toBe(input);
  });

  it("refocuses the message field when refocusPulse changes", async () => {
    const [pulse, setPulse] = createSignal(0);
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        refocusPulse={pulse()}
        onSend={vi.fn()}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    input.blur();
    expect(document.activeElement).not.toBe(input);

    setPulse((n) => n + 1);
    await Promise.resolve();
    expect(document.activeElement).toBe(input);
  });

  it("does not re-focus on refocusPulse when the message field already has focus", async () => {
    const [pulse, setPulse] = createSignal(0);
    const focusSpy = vi.spyOn(HTMLTextAreaElement.prototype, "focus");
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        refocusPulse={pulse()}
        onSend={vi.fn()}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;

    setPulse(1);
    await Promise.resolve();
    expect(document.activeElement).toBe(input);
    const callsAfterFirst = focusSpy.mock.calls.length;

    setPulse(2);
    await Promise.resolve();
    expect(focusSpy.mock.calls.length).toBe(callsAfterFirst);
    focusSpy.mockRestore();
  });

  it("does not steal focus from in-view Find on refocusPulse", async () => {
    const {
      openFind,
      registerFindableView,
      resetFindControllerForTests,
      setPrimaryFindableView,
    } = await import("../../find/find-controller.ts");
    const {
      registerFocusRegion,
      releaseFocusRegion,
      resetFocusRegionsForTests,
    } = await import("../../shortcuts/focus-region.ts");
    resetFindControllerForTests();
    resetFocusRegionsForTests();
    const root = document.createElement("div");
    const findInput = document.createElement("input");
    findInput.dataset.testid = "find-bar-input";
    const findBar = document.createElement("div");
    findBar.dataset.testid = "find-bar";
    findBar.appendChild(findInput);
    document.body.appendChild(root);
    document.body.appendChild(findBar);
    const findClaim = {};
    registerFocusRegion("find", findBar, findClaim);
    const unregister = registerFindableView({
      id: "composer-find-stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("composer-find-stub");
    openFind();
    findInput.focus();
    expect(document.activeElement).toBe(findInput);

    const [pulse, setPulse] = createSignal(0);
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        focusWhen="sess-1"
        refocusPulse={pulse()}
        onSend={vi.fn()}
      />
    ));
    const composer = getByTestId("chat-composer") as HTMLTextAreaElement;

    await Promise.resolve();
    expect(document.activeElement).toBe(findInput);

    setPulse(1);
    await Promise.resolve();
    expect(document.activeElement).toBe(findInput);
    expect(document.activeElement).not.toBe(composer);

    unregister();
    releaseFocusRegion("find", findClaim);
    resetFindControllerForTests();
    resetFocusRegionsForTests();
    root.remove();
    findBar.remove();
  });

  it("does not steal focus from the Settings stage on refocusPulse", async () => {
    const {
      registerFocusRegion,
      releaseFocusRegion,
      resetFocusRegionsForTests,
    } = await import("../../shortcuts/focus-region.ts");
    resetFocusRegionsForTests();
    const settings = document.createElement("section");
    settings.className = "den-settings-panel";
    settings.tabIndex = -1;
    document.body.appendChild(settings);
    const settingsClaim = {};
    registerFocusRegion("settings", settings, settingsClaim);
    settings.focus();

    const [pulse, setPulse] = createSignal(0);
    const { getByTestId, unmount } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        refocusPulse={pulse()}
        onSend={vi.fn()}
      />
    ));
    const composer = getByTestId("chat-composer") as HTMLTextAreaElement;

    setPulse(1);
    await Promise.resolve();
    expect(document.activeElement).toBe(settings);
    expect(document.activeElement).not.toBe(composer);

    unmount();
    releaseFocusRegion("settings", settingsClaim);
    resetFocusRegionsForTests();
    settings.remove();
  });

  it("does not steal focus from the Files editor on refocusPulse", async () => {
    const {
      registerFocusRegion,
      releaseFocusRegion,
      resetFocusRegionsForTests,
    } = await import("../../shortcuts/focus-region.ts");
    resetFocusRegionsForTests();
    const files = document.createElement("div");
    files.tabIndex = 0;
    document.body.appendChild(files);
    const token = {};
    registerFocusRegion("files", files, token);
    files.focus();
    expect(document.activeElement).toBe(files);

    const [pulse, setPulse] = createSignal(0);
    const { getByTestId, unmount } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        refocusPulse={pulse()}
        onSend={vi.fn()}
      />
    ));
    const composer = getByTestId("chat-composer") as HTMLTextAreaElement;

    setPulse(1);
    await Promise.resolve();
    expect(document.activeElement).toBe(files);
    expect(document.activeElement).not.toBe(composer);

    unmount();
    releaseFocusRegion("files", token);
    resetFocusRegionsForTests();
    files.remove();
  });
});
