import { afterEach, describe, expect, it } from "vitest";
import type { ContributionCommand, ContributionFrameResponse } from "../api/types.ts";
import {
  resetContributionStoreForTest,
  seedContributionFrameForTest,
} from "./contribution-store.ts";
import { crossbarCommands } from "./dispatch.ts";
import { ALL_SHELL_FACTS } from "./stock-frame-test.ts";
import { setShellFactState } from "./shell-facts.ts";

function frameWith(commands: ContributionCommand[]): ContributionFrameResponse {
  return {
    frame_revision: "frame-1",
    commands,
    menus: [],
    keybindings: [],
    binding_defaults: [],
    editor_actions: [],
    configuration: [],
    requirements: [],
    search_sources: [],
    operations: [],
    themes: [],
    notes: [],
  };
}

function command(
  id: string,
  extras: Partial<ContributionCommand> = {},
): ContributionCommand {
  return {
    id,
    provider: "acme/kit",
    title: id,
    executor: "host",
    invocation: "project",
    action_kind: "navigate",
    icon: "launcher",
    result_treatment: "effect",
    ...extras,
  };
}

afterEach(() => {
  resetContributionStoreForTest();
});

describe("crossbarCommands", () => {
  it("offers a command with no menu row", () => {
    seedContributionFrameForTest(frameWith([command("acme/kit:explain")]));
    setShellFactState(ALL_SHELL_FACTS);
    expect(crossbarCommands().map((c) => c.id)).toEqual(["acme/kit:explain"]);
  });

  it("hides a command that sets palette: false", () => {
    seedContributionFrameForTest(
      frameWith([command("acme/kit:modal", { palette: false })]),
    );
    setShellFactState(ALL_SHELL_FACTS);
    expect(crossbarCommands()).toEqual([]);
  });

  it("hides a command whose when evaluates false", () => {
    seedContributionFrameForTest(
      frameWith([
        command("acme/kit:gated", { when: { fact: "editor_has_selection" } }),
      ]),
    );
    setShellFactState({
      ...ALL_SHELL_FACTS,
      editor: {
        active: true,
        editable: true,
        hasSelection: false,
        symbol: "symbol",
        findingId: "finding",
        language: "typescript",
      },
    });
    expect(crossbarCommands()).toEqual([]);
  });
});
