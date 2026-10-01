import { describe, expect, it } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { DenButton, type DenButtonVariant } from "./DenButton.tsx";
import { DenField } from "./DenField.tsx";
import { DenInput } from "./DenInput.tsx";
import { DenSelect } from "./DenSelect.tsx";
import { DenTextarea } from "./DenTextarea.tsx";
import { DenCheckbox } from "./DenCheckbox.tsx";
import { DenRadio } from "./DenRadio.tsx";

describe("DenButton", () => {
  it("renders primary variant with btn-primary utility", () => {
    render(() => (
      <DenButton variant="primary" data-testid="save">
        Save
      </DenButton>
    ));
    const btn = screen.getByTestId("save");
    expect(btn.className).toContain("btn-primary");
    expect(btn.className).not.toContain("btn-ghost");
    expect(btn.textContent).toBe("Save");
  });

  it("maps primary+compact to btn-primary-compact", () => {
    render(() => (
      <DenButton variant="primary" compact data-testid="add">
        Add
      </DenButton>
    ));
    const btn = screen.getByTestId("add");
    expect(btn.className).toContain("btn-primary-compact");
    expect(btn.className).not.toContain("btn-compact");
    expect(btn.className).not.toMatch(/(^|\s)btn-primary(\s|$)/);
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

  it("maps danger variant to btn-danger", () => {
    render(() => (
      <DenButton variant="danger" data-testid="danger">
        Reset
      </DenButton>
    ));
    const btn = screen.getByTestId("danger");
    expect(btn.className).toContain("btn-danger");
    expect(btn.className).not.toContain("btn-secondary");
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
  it("wraps control with den-field and label text", () => {
    render(() => (
      <DenField label="Apply preset" data-testid="field">
        <DenSelect
          aria-label="Apply preset"
          data-testid="preset"
          options={[{ value: "", label: "Choose…" }]}
        />
      </DenField>
    ));
    const field = screen.getByTestId("field");
    expect(field.className).toContain("den-field");
    expect(field.textContent).toContain("Apply preset");
    expect(screen.getByTestId("preset").className).toContain("den-select");
  });

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
    expect(screen.getByTestId("field").textContent).toContain("Enter a URL.");
    expect(screen.getByTestId("field").textContent).not.toContain("Local server");
  });
});

describe("DenInput", () => {
  it("renders default and rules kinds", () => {
    render(() => (
      <>
        <DenInput data-testid="default" value="a" readOnly />
        <DenInput kind="rules" data-testid="rules" value="b" readOnly />
      </>
    ));
    expect(screen.getByTestId("default").className).toContain("den-input");
    expect(screen.getByTestId("rules").className).toContain("den-rules-input");
  });
});

describe("DenTextarea", () => {
  it("carries the field recipe and merges the caller class", () => {
    render(() => (
      <DenTextarea class="den-ignore-secret__value" data-testid="area" rows={2} value="a" readOnly />
    ));
    expect(screen.getByTestId("area").className).toContain("den-input");
    expect(screen.getByTestId("area").className).toContain("den-ignore-secret__value");
  });
});

describe("DenCheckbox", () => {
  it("forwards class on the label row", () => {
    render(() => (
      <DenCheckbox class="den-settings-hint" data-testid="cb">
        Overlay
      </DenCheckbox>
    ));
    const input = screen.getByTestId("cb");
    expect(input.closest("label")?.className).toContain("den-settings-hint");
    expect(input.closest("label")?.className).toContain("den-choice-label");
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
