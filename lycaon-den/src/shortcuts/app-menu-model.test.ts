import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { buildAppMenuSpec } from "./app-menu-model.ts";
import { contributionCommand } from "../contributions/dispatch.ts";
import {
  resetContributionStoreForTest,
  seedContributionFrameForTest,
} from "../contributions/contribution-store.ts";
import { setShellFactState } from "../contributions/shell-facts.ts";
import {
  ALL_SHELL_FACTS,
  seedStockFrame,
  stockBindingId,
  stockId,
} from "../contributions/stock-frame-test.ts";
import type { ContributionFrameResponse } from "../api/types.ts";

const CONDITIONAL_MENU: ContributionFrameResponse = {
  frame_revision: "menus",
  commands: [
    {
      id: "acme/pack:always",
      provider: "acme/pack",
      title: "Always",
      scope: "global",
      executor: "den",
      invocation: "den",
      action_kind: "native_ui",
      handler_id: "settings.open",
      icon: "settings",
      result_treatment: "effect",
    },
    {
      id: "acme/pack:placed-when-files",
      provider: "acme/pack",
      title: "Only on Files",
      scope: "global",
      executor: "den",
      invocation: "den",
      action_kind: "native_ui",
      handler_id: "settings.open",
      icon: "settings",
      result_treatment: "effect",
    },
  ],
  menus: [
    {
      id: "acme/pack:menu-always",
      slot: "app_menu.view",
      command: "acme/pack:always",
    },
    {
      id: "acme/pack:menu-conditional",
      slot: "app_menu.view",
      command: "acme/pack:placed-when-files",
      when: { fact: "files_stage_active" },
    },
  ],
  keybindings: [],
  binding_defaults: [],
  editor_actions: [],
  themes: [],
  configuration: [],
  requirements: [],
  search_sources: [],
  operations: [],
  notes: [],
};

const MULTI_DECLARATION_MENU: ContributionFrameResponse = {
  ...CONDITIONAL_MENU,
  keybindings: [
    {
      id: "acme/pack:key-always-primary",
      command: "acme/pack:always",
      scope: "global",
      allow_in_input: true,
      bindings: { macos: ["Mod+J"] },
    },
    {
      id: "acme/pack:key-always-files",
      command: "acme/pack:always",
      scope: "files",
      allow_in_input: false,
      bindings: { macos: ["Mod+Shift+J"] },
    },
  ],
  binding_defaults: [
    {
      platform: "macos",
      scope: "global",
      chord: "Mod+J",
      active: "acme/pack:key-always-primary",
      candidates: ["acme/pack:key-always-primary"],
    },
    {
      platform: "macos",
      scope: "files",
      chord: "Mod+Shift+J",
      active: "acme/pack:key-always-files",
      candidates: ["acme/pack:key-always-files"],
    },
  ],
};

const titles = (menu: string) =>
  buildAppMenuSpec({ platform: "macos" })
    .find((spec) => spec.menu === menu)
    ?.groups.flat()
    .map((item) => item.title) ?? [];

afterEach(() => {
  resetContributionStoreForTest();
});

describe("menu placement conditions", () => {
  beforeEach(() => {
    seedContributionFrameForTest(CONDITIONAL_MENU);
  });

  it("keeps a placement whose condition holds", () => {
    setShellFactState({ ...ALL_SHELL_FACTS, filesStageActive: true });
    expect(titles("view")).toEqual(["Always", "Only on Files"]);
  });

  it("drops a placement whose condition does not hold", () => {
    setShellFactState({ ...ALL_SHELL_FACTS, filesStageActive: false });
    expect(titles("view")).toEqual(["Always"]);
  });

  it("hides irrelevant commands and keeps relevant disabled commands", () => {
    seedContributionFrameForTest({
      ...CONDITIONAL_MENU,
      commands: CONDITIONAL_MENU.commands.map((command) => command.id.endsWith(":always")
        ? { ...command, when: { fact: "project_open" } }
        : { ...command, enablement: { fact: "editor_has_selection" } }),
    });
    setShellFactState({
      ...ALL_SHELL_FACTS,
      projectOpen: false,
      filesStageActive: true,
      editor: { ...ALL_SHELL_FACTS.editor!, hasSelection: false },
    });
    const items = buildAppMenuSpec({ platform: "macos" })
      .find((spec) => spec.menu === "view")?.groups.flat() ?? [];
    expect(items.map((item) => item.title)).toEqual(["Only on Files"]);
    expect(items[0]?.enabled).toBe(false);
  });
});

describe("menu toggle state", () => {
  const TOGGLE_MENU: ContributionFrameResponse = {
    ...CONDITIONAL_MENU,
    menus: [
      {
        id: "acme/pack:menu-sidebar",
        slot: "app_menu.view",
        command: "acme/pack:always",
        label: "Show sidebar",
        state: { fact: "sidebar_expanded" },
        state_label: "Hide sidebar",
      },
      {
        id: "acme/pack:menu-split",
        slot: "app_menu.view",
        command: "acme/pack:placed-when-files",
        label: "Split with conversation",
        state: { fact: "split_live" },
      },
    ],
  };
  const item = (title: string) =>
    buildAppMenuSpec({ platform: "macos" })
      .find((spec) => spec.menu === "view")
      ?.groups.flat()
      .find((candidate) => candidate.title === title);

  beforeEach(() => {
    seedContributionFrameForTest(TOGGLE_MENU);
  });

  it("reads the state label while the state holds", () => {
    setShellFactState({ ...ALL_SHELL_FACTS, sidebarExpanded: true });
    expect(item("Hide sidebar")).toBeDefined();
    setShellFactState({ ...ALL_SHELL_FACTS, sidebarExpanded: false });
    expect(item("Show sidebar")).toBeDefined();
    expect(item("Show sidebar")?.checked).toBeUndefined();
  });

  it("projects the context hide action and its shortcut", () => {
    seedStockFrame();
    setShellFactState({ ...ALL_SHELL_FACTS, contextCollapsed: false });
    expect(item("Hide context")?.binding).toBe("Mod+Alt+Shift+B");
    setShellFactState({ ...ALL_SHELL_FACTS, contextCollapsed: true });
    expect(item("Show context")).toBeDefined();
  });

  it("checks a stateful placement that has no state label", () => {
    setShellFactState({ ...ALL_SHELL_FACTS, splitLive: true });
    expect(item("Split with conversation")?.checked).toBe(true);
    setShellFactState({ ...ALL_SHELL_FACTS, splitLive: false });
    expect(item("Split with conversation")?.checked).toBe(false);
  });
});

describe("menu item ids", () => {
  beforeEach(() => {
    seedStockFrame();
    setShellFactState({ ...ALL_SHELL_FACTS });
  });

  it("name frame commands, which is what the click path dispatches", () => {
    const items = buildAppMenuSpec({ platform: "macos" }).flatMap((spec) =>
      spec.groups.flat(),
    );
    expect(items.length).toBeGreaterThan(0);
    for (const item of items) {
      expect(contributionCommand(item.id)).not.toBeNull();
    }
  });

  it("projects File summaries as a checked View menu toggle", () => {
    const summaries = () =>
      buildAppMenuSpec({ platform: "macos" })
        .find((spec) => spec.menu === "view")
        ?.groups.flat()
        .find((candidate) => candidate.id === stockId("files-toggle-summaries"));
    expect(summaries()).toMatchObject({
      title: "File summaries",
      checked: true,
      enabled: true,
    });

    setShellFactState({ ...ALL_SHELL_FACTS, fileSummariesOn: false });
    expect(summaries()).toMatchObject({ checked: false, enabled: true });

    // Until the setting loads there is nothing to toggle.
    setShellFactState({ ...ALL_SHELL_FACTS, fileSummariesKnown: false });
    expect(summaries()?.enabled).toBe(false);
  });

  it("carries the exact declaration behind a native accelerator", () => {
    const item = buildAppMenuSpec({ platform: "macos" })
      .flatMap((spec) => spec.groups.flat())
      .find((candidate) => candidate.id === stockId("help-shortcuts"));

    expect(item).toMatchObject({
      accelerator: "CmdOrCtrl+Slash",
      bindingId: stockBindingId("help-shortcuts"),
      binding: "Mod+/",
    });
  });

  it("does not install a native accelerator for bare typing chords", () => {
    const spec = buildAppMenuSpec({
      platform: "macos",
      overrides: { [stockBindingId("help-shortcuts")]: "?" },
    });
    const item = spec
      .flatMap((section) => section.groups.flat())
      .find((candidate) => candidate.id === stockId("help-shortcuts"));

    expect(item).toMatchObject({
      accelerator: null,
      bindingId: null,
      binding: null,
    });
  });

  it("does not choose one declaration arbitrarily for a multi-bound command", () => {
    seedContributionFrameForTest(MULTI_DECLARATION_MENU);
    setShellFactState({ ...ALL_SHELL_FACTS });
    const item = buildAppMenuSpec({ platform: "macos" })
      .flatMap((section) => section.groups.flat())
      .find((candidate) => candidate.id === "acme/pack:always");

    expect(item).toMatchObject({
      accelerator: null,
      bindingId: null,
      binding: null,
    });
  });
});
