import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { DenMenuTrigger } from "./DenMenuTrigger.tsx";
import { DenTriggerGroup } from "./DenTriggerGroup.tsx";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";

function segments(): HTMLButtonElement[] {
  return [...screen.getByRole("toolbar").children] as HTMLButtonElement[];
}

describe("DenTriggerGroup", () => {
  it("renders its triggers as bare sibling segments of one track", () => {
    render(() => (
      <DenTriggerGroup ariaLabel="Search tools" testId="search-tools">
        <DenMenuTrigger
          label="Filter"
          open={false}
          popup="dialog"
          active
          testId="filter"
          onClick={vi.fn()}
        />
        <BrowseOverflowMenu
          testId="recent"
          label="Recent"
          items={[{ label: "needle", onSelect: vi.fn() }]}
        />
      </DenTriggerGroup>
    ));

    const track = screen.getByTestId("search-tools");
    expect(track.getAttribute("role")).toBe("toolbar");
    expect(track.getAttribute("aria-label")).toBe("Search tools");
    // The seam between neighbours resolves on DOM adjacency, so a trigger may
    // not wrap itself in a box once it joins a group.
    const children = [...track.children];
    expect(children.map((child) => child.tagName)).toEqual(["BUTTON", "BUTTON"]);
    for (const child of children) {
      expect(child.className).toContain("den-trigger-group__segment");
      expect(child.className).not.toContain("den-select__trigger");
    }
    expect(screen.getByTestId("filter").getAttribute("aria-pressed")).toBe("true");
  });

  it("keeps the select plate for a trigger outside a group", () => {
    render(() => (
      <DenMenuTrigger
        label="Filter"
        open
        popup="dialog"
        testId="lone-filter"
        onClick={vi.fn()}
      />
    ));

    const trigger = screen.getByTestId("lone-filter");
    expect(trigger.className).toContain("den-select__trigger");
    expect(trigger.className).not.toContain("den-trigger-group__segment");
    expect(trigger.getAttribute("data-open")).toBeNull();
    expect(trigger.parentElement?.getAttribute("data-open")).toBe("true");
  });

  it("marks the open segment so the raise and its seams follow the popup", () => {
    const [open, setOpen] = createSignal(false);
    render(() => (
      <DenTriggerGroup ariaLabel="Search tools">
        <DenMenuTrigger
          label="Filter"
          open={open()}
          popup="dialog"
          testId="filter"
          onClick={() => setOpen((value) => !value)}
        />
      </DenTriggerGroup>
    ));

    const filter = screen.getByTestId("filter");
    expect(filter.getAttribute("data-open")).toBeNull();
    fireEvent.click(filter);
    expect(filter.getAttribute("data-open")).toBe("true");
    expect(filter.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(filter);
    expect(filter.getAttribute("data-open")).toBeNull();
  });

  it("holds one tab stop and moves focus across enabled segments", async () => {
    render(() => (
      <DenTriggerGroup ariaLabel="Search tools">
        <DenMenuTrigger
          label="Filter"
          open={false}
          popup="dialog"
          testId="filter"
          onClick={vi.fn()}
        />
        <BrowseOverflowMenu testId="recent" label="Recent" disabled items={[]} />
        <BrowseOverflowMenu
          testId="export"
          label="Export"
          items={[{ label: "JSONL", onSelect: vi.fn() }]}
        />
      </DenTriggerGroup>
    ));

    const [filter, recent, exportSegment] = segments();
    await waitFor(() => expect(filter?.tabIndex).toBe(0));
    expect(recent?.disabled).toBe(true);
    expect(exportSegment?.tabIndex).toBe(-1);

    filter?.focus();
    fireEvent.keyDown(filter!, { key: "ArrowRight" });
    // A disabled segment keeps its seat in the track but never takes focus.
    expect(document.activeElement).toBe(exportSegment);
    expect(exportSegment?.tabIndex).toBe(0);
    expect(filter?.tabIndex).toBe(-1);

    fireEvent.keyDown(exportSegment!, { key: "ArrowRight" });
    expect(document.activeElement).toBe(filter);
    fireEvent.keyDown(filter!, { key: "ArrowLeft" });
    expect(document.activeElement).toBe(exportSegment);
    fireEvent.keyDown(exportSegment!, { key: "Home" });
    expect(document.activeElement).toBe(filter);
    fireEvent.keyDown(filter!, { key: "End" });
    expect(document.activeElement).toBe(exportSegment);
  });

  it("opens the focused segment on ArrowDown and leaves an open one alone", async () => {
    render(() => (
      <DenTriggerGroup ariaLabel="Search tools">
        <BrowseOverflowMenu
          testId="export"
          label="Export"
          items={[{ label: "JSONL", onSelect: vi.fn() }]}
        />
      </DenTriggerGroup>
    ));

    const [exportSegment] = segments();
    exportSegment?.focus();
    fireEvent.keyDown(exportSegment!, { key: "ArrowDown" });
    const item = await screen.findByRole("menuitem", { name: "JSONL" });
    expect(item).toBeTruthy();

    fireEvent.keyDown(exportSegment!, { key: "ArrowDown" });
    expect(exportSegment?.getAttribute("aria-expanded")).toBe("true");
  });

  it("leaves the tab stop where it is when a segment becomes available", async () => {
    const [empty, setEmpty] = createSignal(true);
    render(() => (
      <DenTriggerGroup ariaLabel="Search tools">
        <BrowseOverflowMenu
          testId="recent"
          label="Recent"
          disabled={empty()}
          items={[]}
        />
        <BrowseOverflowMenu
          testId="export"
          label="Export"
          items={[{ label: "JSONL", onSelect: vi.fn() }]}
        />
      </DenTriggerGroup>
    ));

    const [recent, exportSegment] = segments();
    await waitFor(() => expect(exportSegment?.tabIndex).toBe(0));
    expect(recent?.tabIndex).toBe(-1);

    setEmpty(false);
    await waitFor(() => expect(recent?.disabled).toBe(false));
    expect(exportSegment?.tabIndex).toBe(0);
    expect(recent?.tabIndex).toBe(-1);
  });
});
