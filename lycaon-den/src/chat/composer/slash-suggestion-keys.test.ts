import { describe, expect, it, vi } from "vitest";
import { handleSlashSuggestionKey, type SlashSuggestionList } from "./slash-suggestion-keys.ts";

type Init = { key: string; isComposing?: boolean; keyCode?: number; shiftKey?: boolean; ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean };
function key(init: Init) {
  return { metaKey: false, ctrlKey: false, altKey: false, shiftKey: false, isComposing: false, keyCode: 0, preventDefault: vi.fn(), stopPropagation: vi.fn(), ...init } as unknown as KeyboardEvent & { preventDefault: ReturnType<typeof vi.fn> };
}
function list(count: number, active = 0): SlashSuggestionList & { state: { active: number } } {
  const state = { active };
  return { state, count, active: () => state.active, setActive: vi.fn((index: number) => { state.active = index; }), choose: vi.fn(), dismiss: vi.fn() };
}

describe("slash suggestion keys", () => {
  it("moves through the suggestions with wrapping, Home and End", () => {
    const suggestions = list(3);
    for (const [name, expected] of [["ArrowUp", 2], ["ArrowDown", 0], ["ArrowDown", 1], ["End", 2], ["Home", 0]] as const) {
      const event = key({ key: name });
      expect(handleSlashSuggestionKey(event, suggestions)).toBe(true);
      expect(suggestions.state.active).toBe(expected);
      expect(event.preventDefault).toHaveBeenCalledOnce();
    }
  });

  it("chooses the active suggestion and dismisses on Escape", () => {
    const suggestions = list(2, 1);
    expect(handleSlashSuggestionKey(key({ key: "Enter" }), suggestions)).toBe(true);
    expect(suggestions.choose).toHaveBeenCalledWith(1);
    expect(handleSlashSuggestionKey(key({ key: "Escape" }), suggestions)).toBe(true);
    expect(suggestions.dismiss).toHaveBeenCalledOnce();
  });

  it("leaves composition, modified keys, other keys and an empty list alone", () => {
    const suggestions = list(2);
    for (const init of [{ key: "Enter", isComposing: true }, { key: "Enter", keyCode: 229 }, { key: "Escape", keyCode: 229 }, { key: "ArrowDown", shiftKey: true }, { key: "Enter", ctrlKey: true }, { key: "a" }]) {
      const event = key(init);
      expect(handleSlashSuggestionKey(event, suggestions), JSON.stringify(init)).toBe(false);
      expect(event.preventDefault).not.toHaveBeenCalled();
    }
    expect(handleSlashSuggestionKey(key({ key: "Enter" }), list(0))).toBe(false);
    expect(suggestions.choose).not.toHaveBeenCalled();
    expect(suggestions.dismiss).not.toHaveBeenCalled();
  });
});
