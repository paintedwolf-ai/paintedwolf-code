import { describe, expect, it, vi } from "vitest";

vi.mock("../shortcuts/display-binding-for.ts", () => ({
  handlerMatchesEvent: (commandId: string, event: { command?: string }) => event.command === commandId,
}));

import { handleCrossbarPanelKey, type CrossbarPanelKeyActions } from "./crossbar-panel-keys.ts";
import { cycleCrossbarMode } from "./crossbar-model.ts";
import { SEARCH_MODE_JUMP_COMMANDS } from "./crossbar-modes.ts";

function key(init: { key?: string; command?: string; isComposing?: boolean; keyCode?: number }) {
  return { key: "", isComposing: false, keyCode: 0, preventDefault: vi.fn(), stopPropagation: vi.fn(), ...init } as unknown as KeyboardEvent & { preventDefault: ReturnType<typeof vi.fn>; stopPropagation: ReturnType<typeof vi.fn> };
}
function actions(): CrossbarPanelKeyActions {
  const mode = SEARCH_MODE_JUMP_COMMANDS[0]!.mode;
  return { dismiss: vi.fn(), activeMode: () => mode, selectMode: vi.fn(), toggleMatch: vi.fn(), escalate: vi.fn() };
}

describe("Crossbar panel keys", () => {
  it("dismisses on Escape without letting it reach the shell", () => {
    const panel = actions();
    const event = key({ key: "Escape" });
    handleCrossbarPanelKey(event, panel);
    expect(panel.dismiss).toHaveBeenCalledOnce();
    expect(event.stopPropagation).toHaveBeenCalledOnce();
  });

  it("cycles and jumps modes, toggles matching, and escalates through their commands", () => {
    const panel = actions();
    handleCrossbarPanelKey(key({ command: "search.mode.next" }), panel);
    handleCrossbarPanelKey(key({ command: "search.mode.prev" }), panel);
    const last = SEARCH_MODE_JUMP_COMMANDS.at(-1)!;
    handleCrossbarPanelKey(key({ command: last.commandId }), panel);
    expect(vi.mocked(panel.selectMode).mock.calls.map(([mode]) => mode)).toEqual([
      cycleCrossbarMode(panel.activeMode(), 1), cycleCrossbarMode(panel.activeMode(), -1), last.mode,
    ]);
    for (const [command, toggle] of [["search.toggleMatchCase", "caseSensitive"], ["search.toggleWholeWord", "wholeWord"], ["search.toggleRegex", "regex"]] as const) {
      const event = key({ command });
      handleCrossbarPanelKey(event, panel);
      expect(panel.toggleMatch).toHaveBeenLastCalledWith(toggle);
      expect(event.preventDefault).toHaveBeenCalledOnce();
    }
    handleCrossbarPanelKey(key({ command: "search.escalate" }), panel);
    expect(panel.escalate).toHaveBeenCalledOnce();
  });

  it("leaves composition keys and unbound keys alone", () => {
    const panel = actions();
    for (const init of [{ key: "Escape", isComposing: true }, { key: "Escape", keyCode: 229 }, { command: "search.escalate", keyCode: 229 }, { key: "a" }]) {
      const event = key(init);
      handleCrossbarPanelKey(event, panel);
      expect(event.preventDefault, JSON.stringify(init)).not.toHaveBeenCalled();
    }
    expect(panel.dismiss).not.toHaveBeenCalled();
    expect(panel.escalate).not.toHaveBeenCalled();
  });
});
