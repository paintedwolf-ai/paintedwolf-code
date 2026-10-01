import { Composer, pressComposerKey, typeDraft } from "../../test/composer-view-fixture.tsx";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, screen, waitFor } from "@solidjs/testing-library";
import type { WorkflowSummary } from "../../api/types.ts";

describe("Composer", () => {
  it("puts workflow suggestions on the shared anchored surface layer", async () => {
    const view = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        workflowCatalog={[
          {
            id: "plan",
            name: "Plan",
            trigger: "/plan",
          } as WorkflowSummary,
        ]}
        onSend={vi.fn()}
      />
    ));
    const input = view.getByTestId("chat-composer") as HTMLTextAreaElement;

    typeDraft(input, "/");

    const suggestions = screen.getByRole("listbox", {
      name: "Workflow slash commands",
    });
    expect(view.container.contains(suggestions)).toBe(false);
    expect(suggestions.dataset.denAnchoredSurface).toBeTruthy();
    expect(suggestions.style.zIndex).toBe("var(--den-z-anchored-surface)");
    fireEvent.pointerDown(document.body);
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
  });

  it("selects workflow slash suggestions from the composer keyboard", async () => {
    const onSend = vi.fn();
    const view = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        workflowCatalog={[
          { id: "plan", name: "Plan", trigger: "/plan" } as WorkflowSummary,
          { id: "review", name: "Review", trigger: "/review" } as WorkflowSummary,
        ]}
        onSend={onSend}
      />
    ));
    const input = view.getByTestId("chat-composer") as HTMLTextAreaElement;
    typeDraft(input, "/");
    fireEvent.focus(input);

    const options = screen.getAllByRole("option");
    expect(options.map((option) => option.getAttribute("aria-selected"))).toEqual([
      "true",
      "false",
    ]);
    const down = pressComposerKey(input, { key: "ArrowDown", code: "ArrowDown" });
    expect(down.defaultPrevented).toBe(true);
    expect(options[1]?.getAttribute("aria-selected")).toBe("true");

    const enter = pressComposerKey(input, { key: "Enter", code: "Enter" });
    expect(enter.defaultPrevented).toBe(true);
    expect(input.value).toBe("/review ");
    expect(onSend).not.toHaveBeenCalled();
    expect(screen.queryByRole("listbox")).toBeNull();

    typeDraft(input, "/");
    expect(screen.getByRole("listbox")).toBeTruthy();
    pressComposerKey(input, { key: "Escape", code: "Escape" });
    expect(input.value).toBe("/");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("submits empty when a workflow defines empty-send behavior", async () => {
    const onSend = vi.fn();
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        emptySubmitEnabled
        onSend={onSend}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    pressComposerKey(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "" })));
  });

  it("submits empty from the keyboard while streaming holds the Stop slot", async () => {
    const onSend = vi.fn();
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        emptySubmitEnabled
        streaming
        onSend={onSend}
        onStop={vi.fn()}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    pressComposerKey(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "" })));
  });

  it("a ready ask selection arms Send with an empty message field", () => {
    const onSend = vi.fn();
    const [ready, setReady] = createSignal(false);
    const { getByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        streaming
        askPending
        askAnswerReady={ready()}
        onSend={onSend}
        onStop={vi.fn()}
      />
    ));
    // A live unanswered ask keeps Stop primary.
    expect(queryByTestId("composer-send")).toBeNull();
    expect(getByTestId("stop-coordinator")).toBeTruthy();
    expect(queryByTestId("stop-coordinator")?.getAttribute("data-armed")).toBeNull();

    setReady(true);
    expect(queryByTestId("stop-coordinator")).toBeNull();
    const send = getByTestId("composer-send");
    expect(send.getAttribute("aria-label")).toBe("Send answer");
    // A staged pick gives Send its armed state.
    expect(send.classList.contains("den-composer-send--armed")).toBe(true);
    expect(send.getAttribute("data-armed")).toBe("");
    expect(send.hasAttribute("data-tip")).toBe(false);
    fireEvent.click(send);
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({ text: "", attachments: undefined }),
    );
  });

  it("Enter on a radio sends when an ask selection is armed", () => {
    const onSend = vi.fn();
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        askPending
        askAnswerReady
        onSend={onSend}
      />
    ));
    expect(getByTestId("composer-send").getAttribute("data-armed")).toBe("");

    // Choice controls retain focus after selection.
    const radio = document.createElement("input");
    radio.type = "radio";
    document.body.appendChild(radio);
    radio.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({ text: "", attachments: undefined }),
    );
    radio.remove();
  });
});
