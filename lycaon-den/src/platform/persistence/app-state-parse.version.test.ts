import { describe, expect, it, vi } from "vitest";
import { parseAppState } from "./app-state-parse.ts";
import { APP_STATE_VERSION } from "../../../shared/app-state-types.ts";

describe("parseAppState", () => {
  it("identity-loads current version without wiping recents", () => {
    const state = parseAppState({
      version: APP_STATE_VERSION,
      recents: [
        {
          projectId: "p1",
          sessionId: "s1",
          title: "Keep me",
          lastActivityAt: 42,
        },
      ],
    });
    expect(state.version).toBe(APP_STATE_VERSION);
    expect(state.recents).toEqual([
      {
        projectId: "p1",
        sessionId: "s1",
        title: "Keep me",
        lastActivityAt: 42,
      },
    ]);
  });

  it("resets empty for an unsupported version", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const state = parseAppState({
      version: 0,
      recents: [
        { projectId: "p1", sessionId: "s1", title: "Discard me" },
      ],
    });
    expect(state).toEqual({ version: APP_STATE_VERSION, recents: [] });
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });

  it("resets empty for future versions", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const state = parseAppState({
      version: APP_STATE_VERSION + 1,
      recents: [{ projectId: "p1", sessionId: "s1", title: "gone" }],
    });
    expect(state).toEqual({ version: APP_STATE_VERSION, recents: [] });
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });

  it("resets empty for corrupt JSON shape", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    expect(parseAppState(null)).toEqual({
      version: APP_STATE_VERSION,
      recents: [],
    });
    expect(parseAppState("nope")).toEqual({
      version: APP_STATE_VERSION,
      recents: [],
    });
    warn.mockRestore();
  });

  it("resets empty for a missing version", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    expect(parseAppState({ recents: [] })).toEqual({
      version: APP_STATE_VERSION,
      recents: [],
    });
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});
