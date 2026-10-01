import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedSecretAddForm, ManagedSecretValueForm } from "./ManagedSecretForms.tsx";

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
    const value = screen.getByLabelText(mode === "add" ? "Value" : "New value", { exact: true });
    expect(value.tagName).toBe("INPUT");
    expect(value.getAttribute("type")).toBe("password");
    fireEvent.click(screen.getByRole("button", { name: "Show value" }));
    expect(value.getAttribute("type")).toBe("text");
    fireEvent.click(screen.getByRole("button", { name: "Hide value" }));
    expect(value.getAttribute("type")).toBe("password");
    fireEvent.input(value, { target: { value: "🐺🐺🐺" } });
    const save = screen.getByTestId(mode === "add" ? "managed-secret-add-submit" : "managed-secret-value-submit");
    fireEvent.click(save);
    expect(submit).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toContain("at least 4 characters");
    fireEvent.input(value, { target: { value: " 12 " } });
    fireEvent.click(save);
    expect(submit).toHaveBeenCalledExactlyOnceWith(mode === "add"
      ? { name: "Local credential", purpose: "Disposable validation", value: " 12 ", agentUseEndsAt: "" }
      : " 12 ");
  });
});
