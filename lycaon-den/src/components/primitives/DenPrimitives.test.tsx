import { describe, expect, it } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { DenButton, type DenButtonVariant } from "./DenButton.tsx";
import { DenField } from "./DenField.tsx";
import { DenInput } from "./DenInput.tsx";
import { DenRadio } from "./DenRadio.tsx";

describe("DenButton", () => {
  it.each([
    ["primary", false, "btn-primary"],
    ["primary", true, "btn-primary-compact"],
    ["ghost", true, "btn-ghost btn-compact"],
    ["danger", false, "btn-danger"],
  ] as const)("maps %s (compact: %s) to its recipe alone", (variant, compact, recipe) => {
    render(() => <DenButton variant={variant} compact={compact} data-testid="button">Save</DenButton>);
    const button = screen.getByTestId("button");
    expect([...button.classList].filter((name) => name.startsWith("btn-")).sort()).toEqual(recipe.split(" ").sort());
    expect(button.textContent).toBe("Save");
  });

  it("forwards disabled and merges caller class", () => {
    render(() => (
      <DenButton variant="secondary" class="ml-auto" disabled data-testid="x">
        X
      </DenButton>
    ));
    const btn = screen.getByTestId("x") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.className).toContain("btn-secondary");
    expect(btn.className).toContain("ml-auto");
  });

  it("updates class when variant changes", () => {
    const [variant, setVariant] = createSignal<DenButtonVariant>("primary");
    render(() => (
      <DenButton variant={variant()} compact data-testid="seg">
        Standard
      </DenButton>
    ));
    const btn = screen.getByTestId("seg");
    expect(btn.className).toContain("btn-primary-compact");
    setVariant("ghost");
    expect(btn.className).toContain("btn-ghost");
    expect(btn.className).toContain("btn-compact");
    expect(btn.className).not.toContain("btn-primary-compact");
  });
});

describe("DenField", () => {
  it("renders field error instead of hint", () => {
    render(() => (
      <DenField
        label="URL"
        hint="Local server"
        error="Enter a URL."
        data-testid="field"
      >
        <DenInput data-testid="url" />
      </DenField>
    ));
    expect(screen.getByLabelText(/URL/)).toBe(screen.getByTestId("url"));
    expect(screen.getByTestId("field").textContent).toContain("Enter a URL.");
    expect(screen.getByTestId("field").textContent).not.toContain("Local server");
  });
});

describe("DenRadio", () => {
  it("groups radios that share a name", () => {
    render(() => (
      <>
        <DenRadio name="scope" value="project">
          Project
        </DenRadio>
        <DenRadio name="scope" value="global">
          Global
        </DenRadio>
      </>
    ));
    const project = screen.getByRole("radio", {
      name: "Project",
    }) as HTMLInputElement;
    const global = screen.getByRole("radio", {
      name: "Global",
    }) as HTMLInputElement;
    fireEvent.click(project);
    expect(project.checked).toBe(true);
    fireEvent.click(global);
    expect(global.checked).toBe(true);
    expect(project.checked).toBe(false);
  });
});
