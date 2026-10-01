import { afterEach, describe, expect, it, vi } from "vitest";
import type { ContributionCommand, ContributionMenu } from "../api/types.ts";
import { contributionContextMenuItems } from "./context-menu.ts";
import { ALL_SHELL_FACTS } from "./stock-frame-test.ts";
import { STOCK_FRAME } from "./stock-frame.generated.ts";
import { seedContributionFrameForTest, resetContributionStoreForTest } from "./contribution-store.ts";
import { setShellFactState } from "./shell-facts.ts";
import { registerHostCommandInvoker, resetDispatcherForTests } from "../shortcuts/dispatcher.ts";

const command: ContributionCommand = {
  id: "orchard/validation:summarize", provider: "orchard/validation", title: "Summarize selection",
  icon: "tool", executor: "host", invocation: "session", action_kind: "editor_action",
  result_treatment: "receipt", when: { fact: "editor_has_selection" },
  enablement: { fact: "session_idle" },
};
const placement: ContributionMenu = {
  id: "orchard/validation:menu", slot: "editor.context.analysis", command: command.id,
};
function seed(menus: ContributionMenu[] = [placement]) {
  seedContributionFrameForTest({ ...STOCK_FRAME, commands: [command], menus });
  setShellFactState(ALL_SHELL_FACTS);
}
afterEach(() => { resetDispatcherForTests(); resetContributionStoreForTest(); });

describe("contributed context menus", () => {
  it("uses the shared activation lane and rechecks availability before execution", () => {
    seed();
    const invoke = vi.fn();
    registerHostCommandInvoker(invoke);
    const [item] = contributionContextMenuItems([placement.slot]);
    expect(item).toMatchObject({ label: command.title, disabled: false });
    item?.onSelect?.();
    expect(invoke).toHaveBeenCalledWith(command.id);
    invoke.mockClear();
    setShellFactState({ ...ALL_SHELL_FACTS, editor: null });
    item?.onSelect?.();
    expect(invoke).not.toHaveBeenCalled();
    expect(contributionContextMenuItems([placement.slot])).toEqual([]);
  });

  it("keeps disabled commands visible but excludes irrelevant placements and other slots", () => {
    seed([placement, { ...placement, id: "hidden", when: { fact: "session_idle" } },
      { ...placement, id: "other", slot: "app_menu.edit" }]);
    setShellFactState({ ...ALL_SHELL_FACTS, sessionIdle: false });
    expect(contributionContextMenuItems([placement.slot])).toEqual([
      expect.objectContaining({ label: command.title, disabled: true }),
    ]);
  });

  it("projects order, groups, labels and toggle state without changing command identity", () => {
    seed([
      { ...placement, id: "last", group: "b", label: "Last" },
      { ...placement, id: "second", group: "a", order: 2, state: { fact: "session_idle" } },
      { ...placement, id: "first", group: "a", order: 1, state: { fact: "session_idle" }, state_label: "Ready" },
    ]);
    expect(contributionContextMenuItems([placement.slot])).toEqual([
      expect.objectContaining({ label: "Ready" }),
      expect.objectContaining({ label: command.title, checked: true }),
      { separator: true },
      expect.objectContaining({ label: "Last" }),
    ]);
  });

  it("drops placements whose commands disappeared from the current frame", () => {
    seed([{ ...placement, command: "orchard/validation:removed" }]);
    expect(contributionContextMenuItems([placement.slot])).toEqual([]);
  });
});
