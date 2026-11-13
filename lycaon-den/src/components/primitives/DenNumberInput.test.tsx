import { fireEvent, render, screen } from "@solidjs/testing-library";
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

  it("reports a number, null once emptied, and nothing for text that is not a number yet", () => {
    const onNumber = vi.fn();
    const onInput = vi.fn();
    render(() => <DenNumberInput aria-label="Ceiling" step="0.01" onInput={onInput} onNumber={onNumber} />);
    const input = screen.getByRole("spinbutton", { name: "Ceiling" }) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "12.5" } });
    fireEvent.input(input, { target: { value: "" } });
    Object.defineProperty(input, "validity", { configurable: true, value: { badInput: true } });
    fireEvent.input(input, { target: { value: "" } });
    expect(onNumber.mock.calls).toEqual([[12.5], [null]]);
    expect(onInput).toHaveBeenCalledTimes(3);
  });
});
