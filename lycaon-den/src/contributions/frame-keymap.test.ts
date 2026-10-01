/**
 * Scope, input policy, and conditions belong to a binding declaration, not to
 * the command it names. A pack may bind one command twice — different stratum,
 * different rules — and answering per command would apply the loosest one
 * everywhere.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  bindingActiveNow,
  bindingAllowsInput,
  frameKeymapSource,
} from "./frame-keymap.ts";
import {
  resetContributionStoreForTest,
  seedContributionFrameForTest,
} from "./contribution-store.ts";
import { setShellFactState } from "./shell-facts.ts";
import {
  ALL_SHELL_FACTS,
  seedStockFrame,
  stockBindingId,
} from "./stock-frame-test.ts";
import type { ContributionFrameResponse } from "../api/types.ts";
import { resolveKeymap } from "../shortcuts/keymap.ts";

/** One command, bound twice: composer Enter, and a files-stage chord. */
const TWICE_BOUND: ContributionFrameResponse = {
  frame_revision: "twice",
  commands: [
    {
      id: "acme/pack:run",
      provider: "acme/pack",
      title: "Run",
      scope: "composer",
      executor: "den",
      invocation: "den",
      action_kind: "native_ui",
      handler_id: "composer.send",
      icon: "play",
      result_treatment: "effect",
    },
  ],
  menus: [],
  keybindings: [
    {
      id: "acme/pack:key-a-composer",
      command: "acme/pack:run",
      scope: "composer",
      allow_in_input: true,
      bindings: { macos: ["Enter"] },
    },
    {
      id: "acme/pack:key-b-files",
      command: "acme/pack:run",
      scope: "files",
      allow_in_input: false,
      bindings: { macos: ["Mod+R"] },
      when: { fact: "files_stage_active" },
    },
  ],
  binding_defaults: [
    {
      platform: "macos",
      scope: "composer",
      chord: "Enter",
      active: "acme/pack:key-a-composer",
      candidates: ["acme/pack:key-a-composer"],
    },
    {
      platform: "macos",
      scope: "files",
      chord: "Mod+R",
      active: "acme/pack:key-b-files",
      candidates: ["acme/pack:key-b-files"],
    },
  ],
  editor_actions: [],
  themes: [],
  configuration: [],
  requirements: [],
  search_sources: [],
  operations: [],
  notes: [],
};

beforeEach(() => {
  seedContributionFrameForTest(TWICE_BOUND);
  setShellFactState({ ...ALL_SHELL_FACTS });
});

afterEach(() => {
  resetContributionStoreForTest();
});

describe("frameKeymapSource", () => {
  it("preserves declaration identity, defaults, and strata", () => {
    const source = frameKeymapSource(TWICE_BOUND, "macos");
    expect(source.declarations).toEqual([
      {
        id: "acme/pack:key-a-composer",
        commandId: "acme/pack:run",
        scope: "composer",
        defaults: ["Enter"],
      },
      {
        id: "acme/pack:key-b-files",
        commandId: "acme/pack:run",
        scope: "files",
        defaults: ["Mod+R"],
      },
    ]);
  });

  it("drops declarations that do not target this platform", () => {
    const source = frameKeymapSource(TWICE_BOUND, "windows");
    expect(source.declarations).toEqual([]);
  });

  it("overrides one declaration without erasing another declaration for the command", () => {
    const map = resolveKeymap(
      { "acme/pack:key-b-files": "Mod+Shift+R" },
      frameKeymapSource(TWICE_BOUND, "macos"),
    );

    expect(map.byDeclaration.get("acme/pack:key-a-composer")).toEqual(["Enter"]);
    expect(map.byDeclaration.get("acme/pack:key-b-files")).toEqual([
      "Mod+Shift+R",
    ]);
    expect(map.byCommand.get("acme/pack:run")).toEqual([
      "Enter",
      "Mod+Shift+R",
    ]);
  });

  it("matches a leader-relative declaration by its second key", () => {
    // Declarations never name the leader chord — it is a user preference.
    seedStockFrame();
    expect(bindingAllowsInput(stockBindingId("go-composer"))).toBe(
      true,
    );
  });
});

describe("bindingAllowsInput", () => {
  it("answers per binding, not per command", () => {
    expect(bindingAllowsInput("acme/pack:key-a-composer")).toBe(true);
    expect(bindingAllowsInput("acme/pack:key-b-files")).toBe(false);
  });

  it("is false with no hydrated frame", () => {
    resetContributionStoreForTest();
    expect(bindingAllowsInput("acme/pack:key-a-composer")).toBe(false);
  });
});

describe("bindingActiveNow", () => {
  it("holds when the declaration carries no condition", () => {
    expect(bindingActiveNow("acme/pack:key-a-composer")).toBe(true);
  });

  it("gates on the declaration's own condition", () => {
    setShellFactState({ ...ALL_SHELL_FACTS, filesStageActive: false });
    expect(bindingActiveNow("acme/pack:key-b-files")).toBe(false);
    setShellFactState({ ...ALL_SHELL_FACTS, filesStageActive: true });
    expect(bindingActiveNow("acme/pack:key-b-files")).toBe(true);
  });

  it("keeps the unconditional binding firing while the other is gated off", () => {
    setShellFactState({ ...ALL_SHELL_FACTS, filesStageActive: false });
    expect(bindingActiveNow("acme/pack:key-a-composer")).toBe(true);
  });
});
