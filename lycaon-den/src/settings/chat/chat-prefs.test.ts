// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { parseAppState } from "../../platform/persistence/app-state-parse.ts";
import { loadAppState } from "../../platform/persistence/app-state.ts";
import { flushAppState, getAppStateSnapshot, setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import {
  lastStartedModePref,
  messageTimesPref,
  rememberStartedMode,
  resolveMessageTimes,
  saveMessageTimes,
  syncChatPrefsFromSnapshot,
} from "./chat-prefs.ts";

describe("chat-prefs", () => {
  beforeEach(() => {
    localStorage.clear();
    setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
    syncChatPrefsFromSnapshot();
  });

  it("shows message times on hover by default", () => {
    expect(resolveMessageTimes()).toBe("hover");
    expect(messageTimesPref()).toBe("hover");
  });

  it("persists the message times choice through save and load", async () => {
    await saveMessageTimes("always");
    expect(messageTimesPref()).toBe("always");
    expect(getAppStateSnapshot().chat?.messageTimes).toBe("always");
    const loaded = await loadAppState();
    expect(loaded.chat?.messageTimes).toBe("always");
  });

  it("persists the last started mode alongside existing chat preferences", async () => {
    await saveMessageTimes("always");
    rememberStartedMode("ask");
    expect(lastStartedModePref()).toBe("ask");
    await flushAppState();
    expect((await loadAppState()).chat).toEqual({ messageTimes: "always", lastStartedMode: "ask" });
  });

  it("defaults an unrecognized last started mode to Agent", () => {
    setAppStateSnapshot(parseAppState({ ...EMPTY_APP_STATE_V1, chat: { lastStartedMode: "planning" } }));
    syncChatPrefsFromSnapshot();
    expect(lastStartedModePref()).toBe("agent");
  });

  it("reads a stored choice back into the preference", () => {
    setAppStateSnapshot({ ...EMPTY_APP_STATE_V1, chat: { messageTimes: "always" } });
    syncChatPrefsFromSnapshot();
    expect(messageTimesPref()).toBe("always");
  });
});
