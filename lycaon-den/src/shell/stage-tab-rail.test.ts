import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { createStageTabRail } from "./stage-tab-rail.ts";

type StageTabRailFacts = Parameters<typeof createStageTabRail>[0] extends () => infer F ? F : never;
const stageTabRailSession = (facts: StageTabRailFacts) =>
  createRoot((dispose) => {
    const id = createStageTabRail(() => facts, () => "files").sessionId();
    dispose();
    return id;
  });

const offScreen: StageTabRailFacts = {
  presentedSessionId: "s1",
  boundSessionId: "s1",
  conversationShown: false,
  projectStageShown: true,
};

describe("stage tab rail session", () => {
  it("carries the session's tabs while its conversation is off screen beside a project stage", () => {
    expect(stageTabRailSession(offScreen)).toBe("s1");
  });

  it("leaves the tabs to the conversation while it is showing", () => {
    expect(stageTabRailSession({ ...offScreen, conversationShown: true })).toBeNull();
  });

  it("carries nothing outside project stages", () => {
    expect(stageTabRailSession({ ...offScreen, projectStageShown: false })).toBeNull();
  });

  it("carries nothing until the session's own chat serves the tab actions", () => {
    expect(stageTabRailSession({ ...offScreen, boundSessionId: null })).toBeNull();
    expect(stageTabRailSession({ ...offScreen, boundSessionId: "other" })).toBeNull();
    expect(stageTabRailSession({ ...offScreen, presentedSessionId: null, boundSessionId: null })).toBeNull();
  });
});

describe("stage tab rail selection", () => {
  function mount() {
    const [facts, setFacts] = createSignal(offScreen);
    const [stage, setStage] = createSignal<string | null>("files");
    let dispose!: () => void;
    const rail = createRoot((d) => {
      dispose = d;
      return createStageTabRail<string>(facts, stage);
    });
    return { rail, setFacts, setStage, dispose };
  }

  it("keeps the selected tab while the same stage shows the same session", () => {
    const { rail, dispose } = mount();
    rail.selectTab("git");
    expect(rail.openTab()).toBe("git");
    dispose();
  });

  it("closes the panel when another stage takes the column", () => {
    const { rail, setStage, dispose } = mount();
    rail.selectTab("git");
    setStage("search");
    expect(rail.openTab()).toBeNull();
    dispose();
  });

  it("closes the panel when the conversation returns, and stays closed when it leaves again", () => {
    const { rail, setFacts, dispose } = mount();
    rail.selectTab("progress");
    setFacts({ ...offScreen, conversationShown: true });
    expect(rail.sessionId()).toBeNull();
    setFacts(offScreen);
    expect(rail.sessionId()).toBe("s1");
    expect(rail.openTab()).toBeNull();
    dispose();
  });

  it("closes the panel when another session takes over", () => {
    const { rail, setFacts, dispose } = mount();
    rail.selectTab("workers");
    setFacts({ ...offScreen, presentedSessionId: "s2", boundSessionId: "s2" });
    expect(rail.sessionId()).toBe("s2");
    expect(rail.openTab()).toBeNull();
    dispose();
  });
});
