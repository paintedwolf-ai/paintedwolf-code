import { describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { render, screen } from "@solidjs/testing-library";
import { ProgressStrip } from "./ProgressStrip.tsx";
import type { ProgressStep } from "../api/types.ts";

describe("ProgressStrip", () => {
  it("renders block-looking labels with inline formatting at row size", () => {
    const { container } = render(() => <ProgressStrip steps={[
      { state: "pending", label: "## **Synthesis**" },
      { state: "pending", label: "1. Survey dependencies" },
      { state: "pending", label: "> Check the result" },
    ]} />);
    expect(container.querySelector(".markdown-body h2, .markdown-body ol, .markdown-body ul, .markdown-body blockquote")).toBeNull();
    expect(container.querySelector("strong")?.textContent).toBe("Synthesis");
  });

  it("marks only the live checklist as a scrollbar host", () => {
    const { container } = render(() => (
      <ProgressStrip
        steps={[
          { state: "pending", label: "Build the lexer" },
          { state: "pending", label: "Verify the grammar" },
        ]}
      />
    ));

    expect(
      container.querySelector(".progress-strip__body")?.hasAttribute(
        "data-den-scrollport",
      ),
    ).toBe(false);
    expect(
      container.querySelector(".progress-strip__list")?.closest(
        ".progress-strip__list-scroll[data-den-scrollport]",
      ),
    ).toBeTruthy();
    expect(
      container.querySelectorAll("[data-den-scrollport]"),
    ).toHaveLength(1);
  });

  it("does not attach scrollbars to unbounded transcript snapshots", () => {
    const { container } = render(() => (
      <ProgressStrip
        created
        steps={[{ state: "pending", label: "Build the lexer" }]}
      />
    ));

    expect(container.querySelector(".progress-strip__body")).toBeTruthy();
    expect(container.querySelector(".progress-strip__list")).toBeTruthy();
    expect(
      container.querySelectorAll("[data-den-scrollport]"),
    ).toHaveLength(0);
  });

  it("uses one scrollbar host for the embedded checklist", () => {
    const { container } = render(() => (
      <ProgressStrip
        embedded
        steps={[{ state: "pending", label: "Build the lexer" }]}
      />
    ));

    expect(
      container.querySelector(".progress-strip__body")?.hasAttribute(
        "data-den-scrollport",
      ),
    ).toBe(false);
    expect(
      container.querySelector(".progress-strip__columns")?.hasAttribute(
        "data-den-scrollport",
      ),
    ).toBe(true);
    expect(
      container.querySelectorAll("[data-den-scrollport]"),
    ).toHaveLength(1);
  });

  it("does not start the shared clock for a read-only snapshot", () => {
    const interval = vi.spyOn(globalThis, "setInterval");
    const { unmount } = render(() => (
      <ProgressStrip
        completed
        steps={[{ state: "done", label: "Build the lexer" }]}
      />
    ));

    expect(interval).not.toHaveBeenCalled();
    unmount();
    interval.mockRestore();
  });

  it("subscribes to the shared clock only while progress is live", async () => {
    const interval = vi.spyOn(globalThis, "setInterval");
    const clear = vi.spyOn(globalThis, "clearInterval");
    const [completed, setCompleted] = createSignal(false);
    const { unmount } = render(() => (
      <ProgressStrip
        completed={completed()}
        running={!completed()}
        runningSince="2026-01-01T00:00:00Z"
        steps={[{ state: completed() ? "done" : "pending", label: "Build" }]}
      />
    ));

    expect(interval).toHaveBeenCalledTimes(1);
    setCompleted(true);
    await Promise.resolve();
    expect(clear).toHaveBeenCalledTimes(1);

    unmount();
    interval.mockRestore();
    clear.mockRestore();
  });

  it("replaces the scrolling list when a live strip becomes read-only", async () => {
    const [completed, setCompleted] = createSignal(false);
    const { container } = render(() => (
      <ProgressStrip
        completed={completed()}
        steps={[{ state: completed() ? "done" : "pending", label: "Build" }]}
      />
    ));
    const liveList = container.querySelector(".progress-strip__list");
    expect(liveList?.closest("[data-den-scrollport]")).toBeTruthy();

    setCompleted(true);
    await Promise.resolve();
    const snapshotList = container.querySelector(".progress-strip__list");
    expect(snapshotList).not.toBe(liveList);
    expect(snapshotList?.closest("[data-den-scrollport]")).toBeNull();
  });

  it("is hidden when there is no phase chip and no rows", () => {
    render(() => <ProgressStrip steps={[]} />);
    expect(screen.queryByTestId("progress-strip")).toBeNull();
  });

  it("renders the batch phase chip and hides it when closed", () => {
    const { unmount } = render(() => (
      <ProgressStrip batchPhase="integrate" steps={[]} />
    ));
    expect(screen.getByTestId("progress-batch-phase").textContent).toBe(
      "Combining results",
    );
    unmount();
    render(() => <ProgressStrip batchPhase="closed" steps={[]} />);
    expect(screen.queryByTestId("progress-batch-phase")).toBeNull();
  });

  it("renders the coordinator plan checklist with plain and markdown labels", () => {
    render(() => (
      <ProgressStrip
        steps={[
          { state: "pending", label: "Build the lexer" },
          { state: "done", label: "Sketch the **grammar**" },
        ]}
      />
    ));
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(2);
    expect(screen.getByText("Build the lexer")).toBeTruthy();
    expect(screen.getByText("grammar")).toBeTruthy();
    const grammar = screen.getByText("grammar");
    expect(grammar.tagName).toBe("STRONG");
  });

  it("keeps row DOM nodes when a fresh-but-identical plan array arrives", () => {
    const [items, setItems] = createSignal<ProgressStep[]>([
      { state: "pending", label: "Build the lexer" },
    ]);
    render(() => <ProgressStrip steps={items()} />);
    const before = screen.getByText("Build the lexer");

    // Identical updates retain the row node.
    setItems([{ state: "pending", label: "Build the lexer" }]);
    expect(screen.getByText("Build the lexer")).toBe(before);

    // Rendered state changes replace the node.
    setItems([{ state: "done", label: "Build the lexer" }]);
    expect(screen.getByText("Build the lexer")).not.toBe(before);
  });

  it("counts the plan items in the header", () => {
    render(() => (
      <ProgressStrip
        steps={[
          { state: "pending", label: "a" },
          { state: "pending", label: "b" },
        ]}
      />
    ));
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("2");
  });

  it("renders a read-only completed snapshot with no toggle, phase, or worklog", () => {
    const { container } = render(() => (
      <ProgressStrip
        completed
        batchPhase="integrate"
        onOpenWorklog={() => undefined}
        steps={[
          { state: "done", label: "Build the lexer" },
          { state: "done", label: "Sketch the grammar" },
        ]}
      />
    ));
    expect(screen.getByTestId("progress-strip-completed")).toBeTruthy();
    expect(screen.queryByTestId("progress-strip")).toBeNull();
    expect(screen.queryByTestId("progress-strip-toggle")).toBeNull();
    expect(screen.queryByTestId("progress-batch-phase")).toBeNull();
    expect(screen.queryByTestId("worklog-open")).toBeNull();
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(2);
    expect(screen.getByText("Build the lexer")).toBeTruthy();
    const list = container.querySelector(
      ".progress-strip--snapshot .progress-strip__list",
    );
    expect(list).toBeTruthy();
  });

  it("retains a read-only snapshot through a rebuild that carries no steps", () => {
    const [items, setItems] = createSignal<ProgressStep[]>([
      { state: "pending", label: "Add Dockerfile" },
      { state: "pending", label: "Build the container" },
      { state: "pending", label: "Snapshot the UI" },
    ]);
    render(() => <ProgressStrip created steps={items()} />);
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(3);

    // Keep the snapshot through empty rebuild frames.
    setItems([]);
    expect(screen.getByTestId("progress-strip-created")).toBeTruthy();
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(3);
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("3");

    setItems([
      { state: "pending", label: "Add Dockerfile" },
      { state: "pending", label: "Build the container" },
      { state: "pending", label: "Snapshot the UI" },
    ]);
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(3);
  });

  it("replaces read-only rows in one frame", () => {
    const [items, setItems] = createSignal<ProgressStep[]>([
      { state: "done", label: "Old step" },
    ]);
    const { container } = render(() => <ProgressStrip completed steps={items()} />);

    // Snapshot replacements skip the exit transition.
    setItems([{ state: "done", label: "New step" }]);
    expect(screen.getByText("New step")).toBeTruthy();
    expect(screen.queryByText("Old step")).toBeNull();
    expect(container.querySelector(".progress-strip__item--leaving")).toBeNull();
  });

  it("pins the Worklog opener to the left of the progress header", () => {
    const { container } = render(() => (
      <ProgressStrip
        embedded
        batchPhase="dispatch"
        worklogOpen
        onOpenWorklog={() => undefined}
        steps={[{ state: "pending", label: "Plan" }]}
      />
    ));
    const header = container.querySelector(".progress-strip__header");
    const opener = screen.getByTestId("worklog-open");
    expect(header?.firstElementChild).toBe(opener);
    expect(opener.getAttribute("aria-expanded")).toBe("true");
    expect(opener.getAttribute("aria-label")).toBe("Open worklog");
  });

  it("renders an n/a step muted and struck-through", () => {
    render(() => (
      <ProgressStrip
        steps={[
          { state: "done", label: "shipped" },
          { state: "na", label: "dropped scope" },
        ]}
      />
    ));
    const items = screen.getAllByTestId("progress-strip-item");
    const na = items.find((el) => el.getAttribute("data-state") === "na");
    if (!na) throw new Error("expected an n/a item row");
    expect(na.classList.contains("progress-strip__item--na")).toBe(true);
    expect(na.textContent).toContain("dropped scope");
  });

  it("hides elapsed before the first turn (no time banked, not running)", () => {
    render(() => (
      <ProgressStrip
        steps={[{ state: "pending", label: "a" }]}
        activeMs={0}
        running={false}
      />
    ));
    expect(screen.queryByTestId("progress-strip-elapsed")).toBeNull();
  });

  it("shows frozen banked elapsed when paused", () => {
    render(() => (
      <ProgressStrip
        steps={[{ state: "pending", label: "a" }]}
        activeMs={204_000}
        running={false}
      />
    ));
    expect(screen.getByTestId("progress-strip-elapsed").textContent).toBe(
      "3m 24s",
    );
  });

  it("adds the live span to banked time while running", () => {
    const since = new Date(Date.now() - 5_000).toISOString();
    render(() => (
      <ProgressStrip
        steps={[{ state: "pending", label: "a" }]}
        activeMs={60_000}
        running
        runningSince={since}
      />
    ));
    // Allow one second for test execution.
    expect(screen.getByTestId("progress-strip-elapsed").textContent).toMatch(
      /^1m [45]s$/,
    );
  });

  it("renders the context meter with the window percentage and tooltip", () => {
    render(() => (
      <ProgressStrip
        steps={[{ state: "pending", label: "a" }]}
        contextPrompt={40_000}
        contextWindow={200_000}
        compactionThreshold={140_000}
      />
    ));
    const meter = screen.getByTestId("progress-context-meter");
    expect(meter.textContent).toContain("20%");
    expect(meter.getAttribute("data-tier")).toBe("ok");
    expect(meter.getAttribute("aria-label")).toBe(
      "40,000 / 200,000 tokens (20% of context) · compacts at 140,000",
    );
  });

  it("tips the context meter to warn past the compaction threshold", () => {
    render(() => (
      <ProgressStrip
        steps={[{ state: "pending", label: "a" }]}
        contextPrompt={150_000}
        contextWindow={200_000}
        compactionThreshold={140_000}
      />
    ));
    expect(
      screen.getByTestId("progress-context-meter").getAttribute("data-tier"),
    ).toBe("warn");
  });

  it("marks the context meter critical near the window ceiling", () => {
    render(() => (
      <ProgressStrip
        steps={[{ state: "pending", label: "a" }]}
        contextPrompt={190_000}
        contextWindow={200_000}
      />
    ));
    expect(
      screen.getByTestId("progress-context-meter").getAttribute("data-tier"),
    ).toBe("critical");
  });

  it("hides the context meter when the window is unknown", () => {
    render(() => (
      <ProgressStrip
        steps={[{ state: "pending", label: "a" }]}
        contextPrompt={40_000}
        contextWindow={0}
      />
    ));
    expect(screen.queryByTestId("progress-context-meter")).toBeNull();
  });

  it("keeps completed rows in the embedded panel until archived", () => {
    const [items, setItems] = createSignal<ProgressStep[]>([
      { state: "pending", label: "Ship auth" },
    ]);
    render(() => <ProgressStrip steps={items()} embedded />);
    setItems([{ state: "done", label: "Ship auth" }]);
    expect(screen.getByTestId("progress-strip-item")).toBeTruthy();
    expect(screen.getByTestId("progress-strip-item").getAttribute("data-state")).toBe(
      "done",
    );
  });

  it("drops completed rows from the embedded panel once archived in chat", () => {
    const [items] = createSignal<ProgressStep[]>([
      { state: "done", label: "Ship auth" },
    ]);
    const [archived, setArchived] = createSignal(false);
    render(() => (
      <ProgressStrip
        steps={items()}
        embedded
        archivedInTranscript={archived()}
      />
    ));
    expect(screen.getByTestId("progress-strip-item")).toBeTruthy();
    setArchived(true);
    expect(screen.queryByTestId("progress-strip-item")).toBeNull();
  });

  it("fades out a row once archived outside the embedded panel", async () => {
    vi.useFakeTimers();
    const [items] = createSignal<ProgressStep[]>([
      { state: "done", label: "Ship auth" },
    ]);
    const [archived, setArchived] = createSignal(false);
    render(() => (
      <ProgressStrip
        steps={items()}
        archivedInTranscript={archived()}
      />
    ));
    const row = () => screen.getByTestId("progress-strip-item");
    expect(row().classList.contains("progress-strip__item--leaving")).toBe(false);

    setArchived(true);
    expect(row().classList.contains("progress-strip__item--done")).toBe(true);
    expect(row().classList.contains("progress-strip__item--leaving")).toBe(true);

    await vi.advanceTimersByTimeAsync(240);
    expect(screen.queryByTestId("progress-strip-item")).toBeNull();
    vi.useRealTimers();
  });

  it("keeps a done row visible while the plan is still in flight", () => {
    render(() => (
      <ProgressStrip
        steps={[
          { state: "done", label: "Ship auth" },
          { state: "pending", label: "Verify" },
        ]}
      />
    ));
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(2);
  });

  it("counts only open rows in the live header", () => {
    render(() => (
      <ProgressStrip
        steps={[
          { state: "done", label: "a" },
          { state: "pending", label: "b" },
        ]}
      />
    ));
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("1");
  });

  it("renders mid-run plan delta rows as a read-only strip", () => {
    render(() => (
      <ProgressStrip
        deltaChanges={[
          { kind: "done", label: "Ship auth", state: "done" },
          { kind: "created", label: "Verify build", state: "pending" },
        ]}
      />
    ));
    expect(screen.getByTestId("progress-strip-delta")).toBeTruthy();
    expect(screen.getByText("Updated")).toBeTruthy();
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(2);
  });

  it("renders a large update as one aggregate row", () => {
    render(() => (
      <ProgressStrip
        summary={{
          change_count: 16,
          total_steps: 48,
          pending: 32,
          done: 16,
          na: 0,
        }}
      />
    ));
    expect(screen.getByTestId("progress-strip-delta")).toBeTruthy();
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("16");
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(1);
    expect(screen.getByText(/16 progress items updated/)).toBeTruthy();
    expect(screen.getByText(/48 total/)).toBeTruthy();
  });

  it("renders a read-only created checklist snapshot without scrolling on mount", async () => {
    vi.useFakeTimers();
    const items: ProgressStep[] = Array.from({ length: 8 }, (_, i) => ({
      state: i < 3 ? "done" : "pending",
      label: `Step ${i + 1}`,
    }));
    const { container } = render(() => (
      <ProgressStrip steps={items} created />
    ));
    const scrollEl = container.querySelector(
      ".progress-strip__list",
    ) as HTMLUListElement;
    Object.defineProperty(scrollEl, "scrollHeight", {
      configurable: true,
      value: 480,
    });
    Object.defineProperty(scrollEl, "clientHeight", {
      configurable: true,
      value: 80,
    });
    scrollEl.scrollTop = 400;
    await vi.advanceTimersToNextTimerAsync();
    expect(scrollEl.scrollTop).toBe(400);
    vi.useRealTimers();
  });

  it("renders every embedded checklist row in one scrollable list", () => {
    const items = Array.from({ length: 5 }, (_, i) => ({
      state: "pending" as const,
      label: `Step ${i + 1}`,
    }));
    render(() => <ProgressStrip steps={items} embedded />);
    expect(screen.getByTestId("progress-strip-columns")).toBeTruthy();
    expect(screen.getAllByTestId("progress-strip-item")).toHaveLength(5);
    expect(screen.getByText("Step 5")).toBeTruthy();
  });
});
