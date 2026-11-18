import { describe, expect, it } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { ProgressStep } from "../../api/types.ts";
import { ProgressSnapshot } from "./ProgressSnapshot.tsx";

const visibleRows = () =>
  screen.queryAllByTestId("progress-strip-item").filter((row) => !row.closest("[hidden]"));

const restLine = () => screen.getByTestId("progress-strip-rest").textContent;

describe("ProgressSnapshot", () => {
  it("lists the whole plan in a box with no control to close it", () => {
    render(() => (
      <ProgressSnapshot
        kind="created"
        steps={[
          { state: "pending", label: "Build the lexer" },
          { state: "pending", label: "Sketch the grammar" },
        ]}
      />
    ));
    expect(screen.getByTestId("progress-strip-created").textContent).toContain("Progress");
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("2");
    expect(visibleRows()).toHaveLength(2);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("rests an update as one line naming each changed step, then opens into its rows", async () => {
    render(() => (
      <ProgressSnapshot
        kind="updated"
        changes={[
          { kind: "done", label: "Ship auth", state: "done" },
          { kind: "created", label: "Verify build", state: "pending" },
        ]}
      />
    ));
    const opener = screen.getByRole("button", { name: "Updated: Ship auth, Verify build" });
    expect(opener.getAttribute("aria-expanded")).toBe("false");
    expect(restLine()).toBe("✓Ship auth·○Verify build");
    expect(visibleRows()).toHaveLength(0);

    fireEvent.click(opener);
    await Promise.resolve();
    expect(opener.getAttribute("aria-expanded")).toBe("true");
    expect(visibleRows().map((row) => row.textContent)).toEqual(["✓Ship auth", "○Verify build"]);
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("2");

    fireEvent.click(opener);
    await Promise.resolve();
    expect(opener.getAttribute("aria-expanded")).toBe("false");
    expect(visibleRows()).toHaveLength(0);
  });

  it("names a renamed step by both labels and strikes a removed one", () => {
    render(() => (
      <ProgressSnapshot
        kind="updated"
        changes={[
          { kind: "updated", label: "Ship auth and sessions", prev_label: "Ship auth", state: "pending" },
          { kind: "removed", label: "Write the changelog", state: "pending" },
        ]}
      />
    ));
    expect(restLine()).toBe("○Ship auth → Ship auth and sessions·–Write the changelog");
  });

  it("rests a finished plan as its count of done steps", async () => {
    const [steps, setSteps] = createSignal<ProgressStep[]>([
      { state: "done", label: "Build the lexer" },
      { state: "done", label: "Sketch the grammar" },
    ]);
    render(() => <ProgressSnapshot kind="completed" steps={steps()} />);
    expect(restLine()).toBe("✓All 2 steps done");
    expect(screen.getByRole("button", { name: "Done: All 2 steps done" })).toBeTruthy();

    setSteps([
      { state: "done", label: "Build the lexer" },
      { state: "na", label: "Sketch the grammar" },
    ]);
    await Promise.resolve();
    expect(restLine()).toBe("✓1 of 2 steps done");

    setSteps([{ state: "done", label: "Build the lexer" }]);
    await Promise.resolve();
    expect(restLine()).toBe("✓1 step done");
  });

  it("states an aggregate update as one row counting every change", async () => {
    render(() => (
      <ProgressSnapshot
        kind="updated"
        summary={{ change_count: 16, total_steps: 48, pending: 32, done: 16, na: 0 }}
      />
    ));
    expect(restLine()).toBe("○16 progress items updated · 48 total · 32 pending · 16 done");
    fireEvent.click(screen.getByRole("button"));
    await Promise.resolve();
    expect(visibleRows()).toHaveLength(1);
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("16");
  });

  it("keeps its rows through a rebuild that carries no steps", async () => {
    const [steps, setSteps] = createSignal<ProgressStep[]>([
      { state: "pending", label: "Add Dockerfile" },
      { state: "pending", label: "Build the container" },
    ]);
    render(() => <ProgressSnapshot kind="created" steps={steps()} />);
    setSteps([]);
    await Promise.resolve();
    expect(visibleRows()).toHaveLength(2);
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("2");
  });

  it("keeps a row's element when an identical plan arrives", async () => {
    const [steps, setSteps] = createSignal<ProgressStep[]>([
      { state: "pending", label: "Add Dockerfile" },
    ]);
    render(() => <ProgressSnapshot kind="created" steps={steps()} />);
    const row = visibleRows()[0];
    setSteps([{ state: "pending", label: "Add Dockerfile" }]);
    await Promise.resolve();
    expect(visibleRows()[0]).toBe(row);
  });

  it("shows nothing for a row with no steps", () => {
    const { container } = render(() => <ProgressSnapshot kind="updated" changes={[]} />);
    expect(container.textContent).toBe("");
  });

  it("shows how far the plan stands after an update that states it", async () => {
    render(() => (
      <ProgressSnapshot
        kind="updated"
        changes={[{ kind: "done", label: "Ship auth", state: "done" }]}
        summary={{ change_count: 1, total_steps: 5, pending: 3, done: 2, na: 0 }}
      />
    ));
    expect(restLine()).toBe("✓Ship auth");
    expect(screen.getByTestId("progress-strip-standing").textContent).toBe("2 of 5");
    const opener = screen.getByRole("button", { name: "Updated: Ship auth, 2 of 5 done" });

    fireEvent.click(opener);
    await Promise.resolve();
    expect(visibleRows().map((row) => row.textContent)).toEqual(["✓Ship auth"]);
    expect(screen.getByTestId("progress-strip-count").textContent).toBe("1");
  });

  it("lists a plan's steps when its counts arrive with them", () => {
    render(() => (
      <ProgressSnapshot
        kind="created"
        steps={[
          { state: "pending", label: "Build the lexer" },
          { state: "pending", label: "Sketch the grammar" },
        ]}
        summary={{ change_count: 2, total_steps: 2, pending: 2, done: 0, na: 0 }}
      />
    ));
    expect(visibleRows().map((row) => row.textContent)).toEqual(["○Build the lexer", "○Sketch the grammar"]);
    expect(screen.queryByTestId("progress-strip-standing")).toBeNull();
  });

  it("leaves the standing off an update that carries no counts", () => {
    render(() => (
      <ProgressSnapshot kind="updated" changes={[{ kind: "done", label: "Ship auth", state: "done" }]} />
    ));
    expect(screen.queryByTestId("progress-strip-standing")).toBeNull();
  });
});
