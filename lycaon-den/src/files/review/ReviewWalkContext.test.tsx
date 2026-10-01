import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { cleanup, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import type { SourceComparison, SourceWalkEffect } from "../../api/types.ts";
import { ReviewWalkContext } from "./ReviewWalkContext.tsx";
import type { WalkCommandStep, WalkEffectStep, WalkGitStep, WalkOutsideStep } from "../walk/walk-model.ts";

function effectOf(
  id: string,
  path: string,
  overrides: Partial<SourceWalkEffect> = {},
): SourceWalkEffect {
  return sourceEffectFixture({
    id,
    project_id: "p1",
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project",
    root_id: "r1",
    path,
    entry_kind: "file",
    op: "write",
    origin: "external",
    turn: 0,
    ordinal: 5,
    cause: "filesystem_reconcile",
    capture_quality: "reconciled",
    observed_at: "2026-08-26T10:00:00Z",
    ...overrides,
  });
}

function gitStep(effects: SourceWalkEffect[]): WalkGitStep {
  return {
    kind: "git",
    key: "git:t1",
    ordinal: 4,
    label: "git checkout",
    toolCallId: null,
    change: {
      session_id: "", turn: 0, tool_call_id: "", tool_name: "",
      id: "t1",
      root_id: "r1",
      kind: "checkout",
      from_ref: "main",
      to_ref: "feature-x",
      from_commit: "aaaaaaaaaaaa",
      to_commit: "bbbbbbbbbbbb",
      detail: "moving from main to feature-x",
      ordinal: 4,
      observed_at: "2026-08-26T10:00:00Z",
    },
    effects,
  };
}

function commandStep(
  effects: SourceWalkEffect[],
  state: "running" | "ended" | "interrupted" = "ended",
): WalkCommandStep {
  return {
    kind: "command",
    key: "command:w1",
    ordinal: 4,
    label: "command",
    toolCallId: "call-1",
    command: {
      id: "w1",
      session_id: "s1",
      turn: 2,
      tool_call_id: "call-1",
      tool_name: "command",
      command_line: "cargo build --release",
      state,
      admission_mode: state === "ended" ? "scope" : undefined,
      ordinal: 4,
      started_at: "2026-08-26T10:00:00Z",
      ended_at: state === "running" ? undefined : "2026-08-26T10:00:20Z",
    },
    effects,
  };
}

function outsideStep(effects: SourceWalkEffect[]): WalkOutsideStep {
  return {
    kind: "outside",
    key: `outside:${effects[0]?.id ?? ""}`,
    ordinal: effects[0]?.ordinal ?? 0,
    label: "outside",
    toolCallId: null,
    effects,
  };
}

function effectStep(effect: SourceWalkEffect): WalkEffectStep {
  return {
    kind: "effect",
    key: effect.id,
    ordinal: effect.ordinal,
    label: "edit",
    toolCallId: null,
    effect,
  };
}

const comparison: SourceComparison = {
  in_range: true,
  truncated: false,
  location_changed: false,
  before: {
    state: "content",
    size_bytes: 4,
    availability: "available",
    content: "was\n",
  },
  after: {
    state: "content",
    size_bytes: 3,
    availability: "available",
    content: "is\n",
  },
};

function mount(
  step: WalkEffectStep | WalkGitStep | WalkCommandStep | WalkOutsideStep,
  held: SourceComparison | null = comparison,
) {
  render(() => (
    <ReviewWalkContext
      step={step}
      position={1}
      count={1}
      loading={false}
      ready
      comparison={held}
      walkSteps={[step]}
    />
  ));
}

describe("ReviewWalkContext", () => {
  afterEach(cleanup);

  it("presents a command with what it observed and leaves its files to the page", () => {
    const observed = [
      effectOf("cw1", "Cargo.lock", { op: "create", cause: "command_window", command_id: "w1" }),
      effectOf("cw2", "src/gen.rs", { ordinal: 6, cause: "command_window", command_id: "w1" }),
    ];
    mount(commandStep(observed), null);

    const header = screen.getByTestId("review-walk-step");
    expect(header.textContent).toContain("Command");
    expect(header.textContent).toContain("cargo build --release");
    expect(header.textContent).toContain("2 files");
    expect(screen.getByTestId("review-walk-command").textContent)
      .toBe("cargo build --release");
    expect(screen.getByTestId("review-walk-command-observation").textContent)
      .toContain("may not have written them all");
    expect(screen.getByTestId("review-walk-command-tool").textContent).toBe("command");
    expect(screen.getByTestId("review-walk-command-turn").textContent).toBe("2");
    expect(screen.getByTestId("review-walk-command-admission").textContent)
      .toBe("Files inside the source scope");
    expect(screen.queryByTestId("review-walk-command-state")).toBeNull();
    expect(screen.queryByTestId("review-walk-git")).toBeNull();
    expect(screen.queryByTestId("review-walk-path")).toBeNull();
    expect(screen.getByTestId("review-walk-page-hint").textContent)
      .toContain("listed in the editor");
  });

  it("says when a command is still running or was cut short", () => {
    const observed = [effectOf("cw1", "Cargo.lock", { cause: "command_window", command_id: "w1" })];
    mount(commandStep(observed, "running"));
    expect(screen.getByTestId("review-walk-command-state").textContent)
      .toContain("Still running");
    expect(screen.queryByTestId("review-walk-command-admission")).toBeNull();
    cleanup();
    mount(commandStep(observed, "interrupted"));
    expect(screen.getByTestId("review-walk-command-state").textContent)
      .toContain("before this command finished");
  });

  it("presents a git movement as one step and leaves its files to the page", () => {
    const rewritten = [
      effectOf("cg1", "swapped.ts"),
      effectOf("cg2", "deep/other.ts", { ordinal: 6 }),
    ];
    mount(gitStep(rewritten), null);

    const header = screen.getByTestId("review-walk-step");
    expect(header.textContent).toContain("Git checkout");
    expect(header.textContent).toContain("main → feature-x");
    expect(header.textContent).toContain("2 observed files");
    expect(screen.getByTestId("review-walk-git").textContent).toContain(
      "checkout main → feature-x — moving from main to feature-x",
    );
    expect(screen.getByTestId("review-walk-git-commits").textContent)
      .toBe("aaaaaaa → bbbbbbb");
    expect(screen.getByTestId("review-walk-git-detail").textContent)
      .toBe("moving from main to feature-x");
    expect(screen.queryByTestId("review-walk-git-bare")).toBeNull();
    expect(screen.queryByTestId("review-walk-path")).toBeNull();
    expect(screen.getByTestId("review-walk-page-hint")).toBeTruthy();
  });

  it("points a bare movement to its Git review", () => {
    mount(gitStep([]), null);

    expect(screen.getByTestId("review-walk-step").textContent)
      .toContain("Git review");
    expect(screen.getByTestId("review-walk-git-bare").textContent)
      .toContain("Open the Git review");
    expect(screen.queryByTestId("review-walk-path")).toBeNull();
  });

  it("presents an outside run as one step with its span", () => {
    const changed = [
      effectOf("o1", "gen/x.ts", { observed_at: "2026-08-26T10:00:00Z" }),
      effectOf("o2", "gen/y.ts", { ordinal: 6, observed_at: "2026-08-26T10:00:00Z" }),
    ];
    mount(outsideStep(changed), null);

    const header = screen.getByTestId("review-walk-step");
    expect(header.textContent).toContain("Outside the app");
    expect(header.textContent).toContain("2 files");
    expect(screen.getByTestId("review-walk-outside-observation").textContent)
      .toContain("Nothing in this chat wrote them");
    expect(screen.queryByTestId("review-walk-git")).toBeNull();
    expect(screen.queryByTestId("review-walk-command")).toBeNull();
    expect(screen.queryByTestId("review-walk-path")).toBeNull();
  });

  // Step changes update the mounted detail block.
  it("follows the walk when the step advances", () => {
    const first = effectStep(
      effectOf("c1", "src/a.ts", { origin: "agent", turn: 3, tool_name: "edit" }),
    );
    const second = effectStep(
      effectOf("c2", "src/b.ts", {
        origin: "agent",
        turn: 9,
        tool_name: "write",
        from_path: "src/old-b.ts",
        ordinal: 6,
      }),
    );
    const [step, setStep] = createSignal<WalkEffectStep>(first);

    render(() => (
      <ReviewWalkContext
        step={step()}
        position={1}
        count={2}
        loading={false}
        ready
        comparison={comparison}
        walkSteps={[first, second]}
      />
    ));

    expect(screen.getByTestId("review-walk-path").textContent).toBe("src/a.ts");
    expect(screen.getByTestId("review-walk-turn").textContent).toBe("3");
    expect(screen.getByTestId("review-walk-tool").textContent).toBe("edit");
    expect(screen.queryByText("src/old-b.ts")).toBeNull();

    setStep(second);

    expect(screen.getByTestId("review-walk-path").textContent).toBe("src/b.ts");
    expect(screen.getByTestId("review-walk-turn").textContent).toBe("9");
    expect(screen.getByTestId("review-walk-tool").textContent).toBe("write");
    expect(screen.getByText("src/old-b.ts")).toBeTruthy();
  });

  it("keeps the plain file presentation for an effect step", () => {
    mount(effectStep(effectOf("c1", "src/a.ts", { origin: "agent" })));

    const header = screen.getByTestId("review-walk-step");
    expect(header.textContent).toContain("a.ts");
    expect(header.textContent).toContain("Modified");
    expect(screen.queryByTestId("review-walk-git")).toBeNull();
    expect(screen.getByTestId("review-walk-path").textContent)
      .toBe("src/a.ts");
  });
});
