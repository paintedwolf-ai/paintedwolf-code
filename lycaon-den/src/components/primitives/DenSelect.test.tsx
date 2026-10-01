import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { DenSelect } from "./DenSelect.tsx";

const OPTIONS = [
  { value: "alpha", label: "Alpha" },
  { value: "blocked", label: "Blocked", disabled: true },
  { value: "bravo", label: "Bravo", description: "Second choice" },
] as const;

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

describe("DenSelect", () => {
  it.each(["pointer", "keyboard"])("holds an open choice while disabled, then resumes through %s", (input) => {
    const [disabled, setDisabled] = createSignal(false);
    const change = vi.fn();
    render(() => <DenSelect aria-label="Preset" options={OPTIONS} value="alpha" disabled={disabled()} onValueChange={change} />);
    fireEvent.click(screen.getByRole("button", { name: "Preset" }));
    const listbox = screen.getByRole("listbox");
    fireEvent.keyDown(listbox, { key: "End" });
    const pick = () => input === "pointer"
      ? fireEvent.click(screen.getByRole("option", { name: /Bravo/ }))
      : fireEvent.keyDown(listbox, { key: "Enter" });
    setDisabled(true);
    for (const option of screen.getAllByRole("option")) {
      expect(option.getAttribute("aria-disabled")).toBe("true");
    }
    pick();
    expect(change).not.toHaveBeenCalled();
    expect(screen.getByRole("listbox")).toBe(listbox);
    setDisabled(false);
    expect(screen.getByRole("option", { name: /Blocked/ }).getAttribute("aria-disabled")).toBe("true");
    pick();
    expect(change).toHaveBeenCalledExactlyOnceWith("bravo");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("opens a themed listbox and commits a pointer choice", async () => {
    const changed: string[] = [];
    render(() => (
      <DenSelect
        aria-label="Preset"
        data-testid="preset"
        options={OPTIONS}
        value="alpha"
        onValueChange={(value) => changed.push(value)}
      />
    ));

    const trigger = screen.getByTestId("preset");
    expect(trigger.tagName).toBe("BUTTON");
    expect(trigger.textContent).toContain("Alpha");
    fireEvent.click(trigger);

    const listbox = screen.getByRole("listbox", { name: "Preset" });
    await waitFor(() => expect(document.activeElement).toBe(listbox));
    expect(
      screen.getByRole("option", { name: /Bravo/ }).getAttribute("aria-selected"),
    ).toBe("false");

    fireEvent.click(screen.getByRole("option", { name: /Bravo/ }));
    expect(changed).toEqual(["bravo"]);
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("reopens without leaking a computation per open", () => {
    // Settings rows change a value several times in one sitting.
    const Example = () => {
      const [value, setValue] = createSignal("alpha");
      return (
        <DenSelect
          data-testid="preset"
          aria-label="Preset"
          options={OPTIONS}
          value={value()}
          onValueChange={setValue}
        />
      );
    };
    render(() => <Example />);
    for (const label of [/Bravo/, /Alpha/, /Bravo/, /Alpha/]) {
      fireEvent.click(screen.getByTestId("preset"));
      fireEvent.click(screen.getByRole("option", { name: label }));
    }
    expect(screen.getByTestId("preset").textContent).toContain("Alpha");
  });

  it("provides controlled value updates without retaining stale display text", () => {
    const Example = () => {
      const [value, setValue] = createSignal("alpha");
      return (
        <DenSelect
          data-testid="preset"
          aria-label="Preset"
          options={OPTIONS}
          value={value()}
          onValueChange={setValue}
        />
      );
    };
    render(() => <Example />);
    fireEvent.click(screen.getByTestId("preset"));
    fireEvent.click(screen.getByRole("option", { name: /Bravo/ }));
    expect(screen.getByTestId("preset").textContent).toContain("Bravo");
  });

  it("supports arrows, Home/End, selection, and skips disabled options", async () => {
    const onValueChange = vi.fn();
    render(() => (
      <DenSelect
        data-testid="preset"
        aria-label="Preset"
        options={OPTIONS}
        value="alpha"
        onValueChange={onValueChange}
      />
    ));
    const trigger = screen.getByTestId("preset");
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    const listbox = screen.getByRole("listbox");
    await waitFor(() => expect(document.activeElement).toBe(listbox));

    fireEvent.keyDown(listbox, { key: "ArrowDown" });
    expect(listbox.getAttribute("aria-activedescendant")).toContain("option-2");
    fireEvent.keyDown(listbox, { key: "Home" });
    expect(listbox.getAttribute("aria-activedescendant")).toContain("option-0");
    fireEvent.keyDown(listbox, { key: "End" });
    expect(listbox.getAttribute("aria-activedescendant")).toContain("option-2");
    fireEvent.keyDown(listbox, { key: "Enter" });
    expect(onValueChange).toHaveBeenCalledOnce();
  });

  it("supports typeahead and restores focus on Escape", async () => {
    render(() => (
      <DenSelect
        data-testid="preset"
        aria-label="Preset"
        options={OPTIONS}
        value="alpha"
      />
    ));
    const trigger = screen.getByTestId("preset");
    fireEvent.keyDown(trigger, { key: "b" });
    const listbox = screen.getByRole("listbox");
    expect(listbox.getAttribute("aria-activedescendant")).toContain("option-2");
    fireEvent.keyDown(listbox, { key: "Escape" });
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("does not open when disabled and closes on an outside mouse press", () => {
    const view = render(() => (
      <DenSelect
        data-testid="preset"
        aria-label="Preset"
        options={OPTIONS}
        value="alpha"
        disabled
      />
    ));
    fireEvent.click(screen.getByTestId("preset"));
    expect(screen.queryByRole("listbox")).toBeNull();
    view.unmount();

    render(() => (
      <DenSelect
        data-testid="active-preset"
        aria-label="Preset"
        options={OPTIONS}
        value="alpha"
      />
    ));
    fireEvent.click(screen.getByTestId("active-preset"));
    expect(screen.getByRole("listbox")).toBeTruthy();
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("takes focus without scrolling its ancestors", async () => {
    const opts: FocusOptions[] = [];
    const focus = HTMLElement.prototype.focus;
    HTMLElement.prototype.focus = function (options?: FocusOptions) {
      if (this.getAttribute("role") === "listbox") opts.push(options ?? {});
      return focus.call(this, options);
    };
    try {
      render(() => (
        <DenSelect
          data-testid="preset"
          aria-label="Preset"
          options={OPTIONS}
          value="alpha"
        />
      ));
      fireEvent.click(screen.getByTestId("preset"));
      await waitFor(() => expect(opts.length).toBeGreaterThan(0));
      expect(opts.every((o) => o.preventScroll === true)).toBe(true);
    } finally {
      HTMLElement.prototype.focus = focus;
    }
  });

  it("never asks an ancestor to scroll it into view", async () => {
    const calls: string[] = [];
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function () {
      calls.push(this.className || this.nodeName);
    };
    try {
      render(() => (
        <DenSelect
          data-testid="preset"
          aria-label="Preset"
          options={OPTIONS}
          value="bravo"
        />
      ));
      fireEvent.click(screen.getByTestId("preset"));
      await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());
      fireEvent.keyDown(screen.getByRole("listbox"), { key: "ArrowDown" });
      await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());
      expect(calls).toEqual([]);
    } finally {
      Element.prototype.scrollIntoView = original;
    }
  });

  it("re-anchors on scroll instead of dismissing", () => {
    render(() => (
      <DenSelect
        data-testid="preset"
        aria-label="Preset"
        options={OPTIONS}
        value="alpha"
      />
    ));

    fireEvent.click(screen.getByTestId("preset"));
    const listbox = screen.getByRole("listbox");

    for (const target of [
      listbox,
      screen.getByRole("option", { name: /Alpha/ }),
      document,
      window,
    ]) {
      target.dispatchEvent(new Event("scroll"));
      expect(screen.queryByRole("listbox")).toBeTruthy();
    }

    fireEvent(window, new Event("resize"));
    expect(screen.queryByRole("listbox")).toBeTruthy();
  });

  it("closes once the trigger leaves the viewport", async () => {
    render(() => (
      <DenSelect
        data-testid="preset"
        aria-label="Preset"
        options={OPTIONS}
        value="alpha"
      />
    ));
    const trigger = screen.getByTestId("preset");
    fireEvent.click(trigger);
    expect(screen.getByRole("listbox")).toBeTruthy();

    trigger.getBoundingClientRect = () =>
      ({ top: -80, bottom: -40, left: 0, width: 120, height: 40 }) as DOMRect;
    document.dispatchEvent(new Event("scroll"));

    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
  });
});
