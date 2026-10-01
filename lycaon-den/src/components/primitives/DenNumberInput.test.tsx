import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { DenNumberInput } from "./DenNumberInput.tsx";

describe("DenNumberInput", () => {
  it("uses one native numeric field without redundant stepper buttons", () => {
    const onInput = vi.fn();
    render(() => (
      <DenNumberInput
        aria-label="Retries"
        value="2"
        min="0"
        max="5"
        onInput={onInput}
      />
    ));

    const input = screen.getByRole("spinbutton", {
      name: "Retries",
    }) as HTMLInputElement;
    expect(input.value).toBe("2");
    expect(input.min).toBe("0");
    expect(input.max).toBe("5");
    expect(screen.queryByRole("button")).toBeNull();
    expect(onInput).not.toHaveBeenCalled();
  });
});
