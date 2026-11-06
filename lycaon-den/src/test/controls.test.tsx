import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { assertNamedControl } from "./controls.ts";

describe("assertNamedControl", () => {
  it("returns a control that exposes the role and a name", () => {
    const view = render(() => (
      <div>
        <button data-testid="text-name">Label</button>
        <button data-testid="aria-name" aria-label="Label" />
      </div>
    ));
    for (const id of ["text-name", "aria-name"]) {
      const control = view.getByTestId(id);
      expect(assertNamedControl(control, "button")).toBe(control);
    }
  });

  it("refuses a control without a name", () => {
    const view = render(() => <button data-testid="unnamed" />);
    expect(() => assertNamedControl(view.getByTestId("unnamed"), "button")).toThrow();
  });

  it("refuses a control with another role", () => {
    const view = render(() => <a data-testid="link" href="https://example.test">Label</a>);
    expect(() => assertNamedControl(view.getByTestId("link"), "button")).toThrow();
  });

  it("does not accept a sibling's name for an unnamed control", () => {
    const view = render(() => (
      <div>
        <button>Other</button>
        <button data-testid="unnamed" />
      </div>
    ));
    expect(() => assertNamedControl(view.getByTestId("unnamed"), "button")).toThrow();
  });
});
