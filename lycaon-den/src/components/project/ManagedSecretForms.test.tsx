import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedSecretAddForm, ManagedSecretValueForm } from "./ManagedSecretForms.tsx";
import { assertNamedControl } from "../../test/controls.ts";

afterEach(cleanup);

describe("managed-secret value entry", () => {
  it.each(["add", "replace", "restore"])("validates and labels the %s field before submission", (mode) => {
    const submit = vi.fn();
    render(() => mode === "add"
      ? <ManagedSecretAddForm busy={false} onCancel={() => undefined} onSubmit={submit} />
      : <ManagedSecretValueForm restoring={mode === "restore"} markedInFile={false} busy={false} onCancel={() => undefined} onSubmit={submit} />);
    if (mode === "add") {
      fireEvent.input(screen.getByTestId("managed-secret-add-name"), { target: { value: "Local credential" } });
      fireEvent.input(screen.getByTestId("managed-secret-add-purpose"), { target: { value: "Disposable validation" } });
    }
    const value = screen.getByTestId(mode === "add" ? "managed-secret-add-value" : "managed-secret-new-value");
    expect(value.tagName).toBe("INPUT");
    expect(value.getAttribute("type")).toBe("password");
    fireEvent.click(assertNamedControl(screen.getByTestId(`${mode === "add" ? "managed-secret-add-value" : "managed-secret-new-value"}-reveal`), "button"));
    expect(value.getAttribute("type")).toBe("text");
    fireEvent.click(assertNamedControl(screen.getByTestId(`${mode === "add" ? "managed-secret-add-value" : "managed-secret-new-value"}-reveal`), "button"));
    expect(value.getAttribute("type")).toBe("password");
    fireEvent.input(value, { target: { value: "🐺🐺🐺" } });
    const save = screen.getByTestId(mode === "add" ? "managed-secret-add-submit" : "managed-secret-value-submit");
    fireEvent.click(save);
    expect(submit).not.toHaveBeenCalled();
    fireEvent.input(value, { target: { value: " 12 " } });
    fireEvent.click(save);
    expect(submit).toHaveBeenCalledExactlyOnceWith(mode === "add"
      ? { name: "Local credential", purpose: "Disposable validation", value: " 12 ", agentUseEndsAt: "" }
      : " 12 ");
  });
});
