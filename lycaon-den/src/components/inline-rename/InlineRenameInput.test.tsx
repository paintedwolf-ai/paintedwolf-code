import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { InlineRenameInput } from "./InlineRenameInput.tsx";

describe("InlineRenameInput", () => {
  it("commits an edited draft", () => {
    const onCommit = vi.fn();
    const onCancel = vi.fn();
    const { getByRole } = render(() => (
      <InlineRenameInput initialValue="Old" onCommit={onCommit} onCancel={onCancel} />
    ));
    const input = getByRole("textbox") as HTMLInputElement;
    fireEvent.input(input, { target: { value: "New" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onCommit).toHaveBeenCalledWith("New");
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("cancels an unchanged draft", () => {
    const onCommit = vi.fn();
    const onCancel = vi.fn();
    const { getByRole } = render(() => (
      <InlineRenameInput initialValue="Old" onCommit={onCommit} onCancel={onCancel} />
    ));
    fireEvent.keyDown(getByRole("textbox"), { key: "Enter" });
    expect(onCommit).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalled();
  });

  it("focuses and selects the name on mount", () => {
    const { getByRole } = render(() => (
      <InlineRenameInput initialValue="Old" onCommit={vi.fn()} onCancel={vi.fn()} />
    ));
    const input = getByRole("textbox") as HTMLInputElement;
    expect(document.activeElement).toBe(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, "Old".length]);
  });

  it("supports caller-specific selection and blur validation", () => {
    const onCommit = vi.fn();
    const onCancel = vi.fn();
    const { getByRole } = render(() => (
      <InlineRenameInput
        initialValue="main.ts"
        selectionRange={() => ({ start: 0, end: 4 })}
        shouldCommitOnBlur={(value) => !value.includes("/")}
        onCommit={onCommit}
        onCancel={onCancel}
      />
    ));
    const input = getByRole("textbox") as HTMLInputElement;
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, 4]);

    fireEvent.input(input, { target: { value: "other/name.ts" } });
    fireEvent.blur(input);
    expect(onCommit).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalledOnce();
  });

  // A session auto-title can land over SSE while the editor is open. An untouched
  // editor must not write its stale seed value back over the fresh title.
  it("does not commit the seed value when the live value changed underneath", () => {
    const onCommit = vi.fn();
    const onCancel = vi.fn();
    const [title, setTitle] = createSignal("New session");
    const { getByRole } = render(() => (
      <InlineRenameInput initialValue={title()} onCommit={onCommit} onCancel={onCancel} />
    ));
    setTitle("Fix login bug");
    fireEvent.blur(getByRole("textbox"));
    expect(onCommit).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalled();
  });
});

it("allows retry and cancellation after a rejected asynchronous rename", async () => {
  const onCommit = vi.fn(async () => false);
  const onCancel = vi.fn();
  const { getByRole } = render(() => <InlineRenameInput initialValue="old.ts" onCommit={onCommit} onCancel={onCancel} />);
  const input = getByRole("textbox");
  fireEvent.input(input, { target: { value: "taken.ts" } });
  fireEvent.keyDown(input, { key: "Enter" });
  await waitFor(() => expect(onCommit).toHaveBeenCalledOnce());
  fireEvent.input(input, { target: { value: "other.ts" } });
  fireEvent.keyDown(input, { key: "Enter" });
  await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(2));
  await Promise.resolve();
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onCancel).toHaveBeenCalledOnce();
});
