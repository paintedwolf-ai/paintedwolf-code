// @vitest-environment jsdom
import { createComputed, createRoot } from "solid-js";
import { afterEach, expect, it } from "vitest";
import { ALL_SHELL_FACTS } from "./stock-frame-test.ts";
import { STOCK_FRAME } from "./stock-frame.generated.ts";
import { setShellFactState } from "./shell-facts.ts";
import { seedContributionFrameForTest, resetContributionStoreForTest } from "./contribution-store.ts";
import { contributionCommandAvailable, crossbarCommands } from "./dispatch.ts";
import { contributionContextMenuItems } from "./context-menu.ts";

afterEach(resetContributionStoreForTest);

it("updates every command projection when selection, presentation or enablement changes", () => {
  const id = "orchard/validation:selection";
  seedContributionFrameForTest({ ...STOCK_FRAME, commands: [{
    id, provider: "orchard/validation", title: "Inspect selection", icon: "tool",
    executor: "host", invocation: "session", action_kind: "editor_action", result_treatment: "receipt",
    when: { fact: "editor_has_selection" }, enablement: { fact: "session_idle" },
  }], menus: [{ id: "orchard/validation:menu", slot: "editor.context.analysis", command: id }] });
  setShellFactState({ ...ALL_SHELL_FACTS, editor: null });
  createRoot((dispose) => {
    let projection: unknown;
    createComputed(() => {
      projection = {
        palette: crossbarCommands().map((command) => command.id),
        menu: contributionContextMenuItems(["editor.context.analysis"]).map((item) => item.disabled),
        shortcut: contributionCommandAvailable(id),
      };
    });
    expect(projection).toEqual({ palette: [], menu: [], shortcut: false });
    setShellFactState(ALL_SHELL_FACTS);
    expect(projection).toEqual({ palette: [id], menu: [false], shortcut: true });
    setShellFactState({ ...ALL_SHELL_FACTS, sessionIdle: false });
    expect(projection).toEqual({ palette: [id], menu: [true], shortcut: false });
    setShellFactState({ ...ALL_SHELL_FACTS, editor: { ...ALL_SHELL_FACTS.editor!, hasSelection: false } });
    expect(projection).toEqual({ palette: [], menu: [], shortcut: false });
    setShellFactState(ALL_SHELL_FACTS);
    expect(projection).toEqual({ palette: [id], menu: [false], shortcut: true });
    setShellFactState({ ...ALL_SHELL_FACTS, editor: null });
    expect(projection).toEqual({ palette: [], menu: [], shortcut: false });
    dispose();
  });
});
