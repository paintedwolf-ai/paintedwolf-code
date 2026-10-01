import { describe, expect, it } from "vitest";
import { createSignal } from "solid-js";
import { render, screen } from "@solidjs/testing-library";
import { NavFold } from "./NavFold.tsx";

describe("NavFold", () => {
  it("removes collapsed descendants from keyboard navigation", () => {
    const [open, setOpen] = createSignal(false);
    render(() => (
      <NavFold open={open()}>
        <button type="button">Action</button>
      </NavFold>
    ));

    const fold = screen.getByRole("button", { name: "Action", hidden: true }).parentElement
      ?.parentElement as HTMLElement;
    expect(fold.getAttribute("aria-hidden")).toBe("true");
    expect(fold.inert).toBe(true);

    setOpen(true);
    expect(fold.getAttribute("aria-hidden")).toBe("false");
    expect(fold.inert).toBe(false);
  });
});
