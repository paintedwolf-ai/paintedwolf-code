import { Composer, pressComposerKey, typeDraft, sendDraft } from "../../test/composer-view-fixture.tsx";
import { describe, expect, it, vi } from "vitest";
import { render, fireEvent } from "@solidjs/testing-library";

describe("Composer", () => {

  it("keeps Enter sending after a crossfaded stage unmounts its composer", () => {
    // The outgoing stage unmounts after the incoming composer takes focus.
    const outgoing = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    fireEvent.focus(outgoing.getByTestId("chat-composer"));

    const onSend = vi.fn();
    const incoming = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-2" onSend={onSend} />
    ));
    const input = incoming.getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.focus(input);
    outgoing.unmount();

    fireEvent.input(input, { target: { value: "hello" } });
    input.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({ text: "hello" }),
    );
    expect(input.value).not.toContain("\n");
  });

  it("Enter sends from the focused composer when another composer mounted later", () => {
    const onSendEarlier = vi.fn();
    const onSendLater = vi.fn();
    const earlier = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={onSendEarlier} />
    ));
    const later = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-2" onSend={onSendLater} />
    ));
    fireEvent.input(later.getByTestId("chat-composer"), { target: { value: "later draft" } });

    const input = earlier.getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "earlier draft" } });
    pressComposerKey(input, { key: "Enter", code: "Enter" });

    expect(onSendEarlier).toHaveBeenCalledWith(
      expect.objectContaining({ text: "earlier draft" }),
    );
    expect(onSendLater).not.toHaveBeenCalled();
  });

  it("Shift+Enter inserts a newline via composer.newline", () => {
    const onSend = vi.fn();
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "line" } });
    input.setSelectionRange(4, 4);
    pressComposerKey(input, { key: "Enter", code: "Enter", shiftKey: true });
    expect(onSend).not.toHaveBeenCalled();
    expect(input.value).toBe("line\n");
  });

  it("↑ recalls the last sent prompt into an empty composer", async () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    await sendDraft(input, "first ask");

    const event = pressComposerKey(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(input.value).toBe("first ask");
    // Handled recall suppresses native arrow navigation.
    expect(event.defaultPrevented).toBe(true);
  });

  it("↑ moves the caret instead of recalling while a draft is being written", () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    typeDraft(input, "line one\nline two");

    const event = pressComposerKey(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(event.defaultPrevented).toBe(false);
    expect(input.value).toBe("line one\nline two");
  });

  it("↑ with no history at all leaves the arrow alone", () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-fresh" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    const event = pressComposerKey(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(event.defaultPrevented).toBe(false);
  });

  it("walks past a recalled multi-line prompt only from its first line", async () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    await sendDraft(input, "older ask");
    await sendDraft(input, "newer\nask");

    pressComposerKey(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(input.value).toBe("newer\nask");

    // The second line keeps native caret navigation.
    input.setSelectionRange(8, 8);
    const inside = pressComposerKey(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(inside.defaultPrevented).toBe(false);
    expect(input.value).toBe("newer\nask");

    // The first line allows recall navigation.
    input.setSelectionRange(2, 2);
    pressComposerKey(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(input.value).toBe("older ask");
  });

  it("↓ walks back out of recall to the composer it started from", async () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    await sendDraft(input, "sent thing");

    pressComposerKey(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(input.value).toBe("sent thing");
    input.setSelectionRange(input.value.length, input.value.length);
    pressComposerKey(input, { key: "ArrowDown", code: "ArrowDown" });
    expect(input.value).toBe("");
  });
});
