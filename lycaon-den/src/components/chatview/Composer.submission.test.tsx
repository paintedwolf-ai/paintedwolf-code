import { Composer, pressComposerKey } from "../../test/composer-view-fixture.tsx";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, waitFor } from "@solidjs/testing-library";
import { composerDraftForSession, setComposerDraft } from "../../chat/composer/composer-drafts.ts";

describe("Composer", () => {

  it("keeps the draft until an asynchronous redirected send succeeds", async () => {
    let settle: ((value: boolean | void) => void) | undefined;
    const onSend = vi.fn(() => new Promise<boolean | void>((resolve) => {
      settle = resolve;
    }));
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-approval" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "Stop and explain" } });
    fireEvent.click(getByTestId("composer-send"));

    expect(input.value).toBe("Stop and explain");
    settle?.(false);
    await waitFor(() => expect(input.value).toBe("Stop and explain"));
  });

  it("submits an asynchronous redirected send only once while it is pending", async () => {
    let settle: ((value: boolean | void) => void) | undefined;
    const onSend = vi.fn(() => new Promise<boolean | void>((resolve) => {
      settle = resolve;
    }));
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-approval"
        approvalRedirect
        onSend={onSend}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "Stop and explain" } });
    fireEvent.click(getByTestId("composer-send"));
    fireEvent.click(getByTestId("composer-send"));

    expect(onSend).toHaveBeenCalledTimes(1);
    expect((getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(true);
    settle?.(false);
    await waitFor(() => {
      expect(input.value).toBe("Stop and explain");
      expect((getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(false);
    });
  });

  it("keeps submission state independent across chat switches", async () => {
    let settleFirst: ((value: boolean | void) => void) | undefined;
    const onSend = vi.fn(({ text }: { text: string }) =>
      text === "from first"
        ? new Promise<boolean | void>((resolve) => {
            settleFirst = resolve;
          })
        : false,
    );
    const [sessionId, setSessionId] = createSignal("sess-first");
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId={sessionId()}
        onSend={onSend}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "from first" } });
    fireEvent.click(getByTestId("composer-send"));
    // A pending submission retains its draft.
    fireEvent.click(getByTestId("composer-send"));
    expect(onSend).toHaveBeenCalledTimes(1);

    setSessionId("sess-second");
    fireEvent.input(input, { target: { value: "from second" } });
    expect((getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(getByTestId("composer-send"));
    expect(onSend).toHaveBeenCalledTimes(2);

    settleFirst?.(false);
  });

  it("does not erase a newer draft when an asynchronous send succeeds", async () => {
    let settle: ((value: boolean | void) => void) | undefined;
    const onSend = vi.fn(() => new Promise<boolean | void>((resolve) => {
      settle = resolve;
    }));
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-approval" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "Stop and explain" } });
    fireEvent.click(getByTestId("composer-send"));
    fireEvent.input(input, { target: { value: "A newer instruction" } });
    settle?.(undefined);

    await waitFor(() => expect(input.value).toBe("A newer instruction"));
  });

  it("keeps the draft and reports a failed send", async () => {
    const { getByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-failed-send"
        onSend={() => Promise.reject(new Error("offline"))}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "keep this" } });
    fireEvent.click(getByTestId("composer-send"));

    expect((await findByTestId("composer-attach-error")).textContent).toBe(
      "Message could not be sent. Try again.",
    );
    expect(input.value).toBe("keep this");
    expect(composerDraftForSession("sess-failed-send")).toBe("keep this");
  });

  it("clears eagerly the moment the send commits, before it settles", async () => {
    const [streaming, setStreaming] = createSignal(false);
    const [pending, setPending] = createSignal(false);
    let settle: ((value: boolean | void) => void) | undefined;
    const onSend = vi.fn((payload: { onPendingSend?: (destination: "transcript" | "queue") => void }) => {
      setPending(true);
      payload.onPendingSend?.("transcript");
      return new Promise<boolean | void>((resolve) => {
        settle = resolve;
      });
    });
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-eager" onSend={onSend}
        streaming={streaming()} pendingTranscriptSend={pending()} activityLabel="Reading files" onStop={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "ship it" } });
    fireEvent.click(getByTestId("composer-send"));

    expect(input.value).toBe("");
    expect(composerDraftForSession("sess-eager")).toBe("");
    await waitFor(() => expect(getByTestId("thinking-indicator").textContent).toBe("Sending"));
    const activity = getByTestId("thinking-indicator");
    setStreaming(true);
    setPending(false);
    expect(getByTestId("thinking-indicator")).toBe(activity);
    expect(activity.textContent).toBe("Reading files");
    settle?.(undefined);
    await waitFor(() => expect(input.value).toBe(""));
  });

  it("restores the draft when an eagerly cleared send later fails", async () => {
    const onSend = vi.fn((payload: { onPendingSend?: (destination: "transcript" | "queue") => void }) => {
      payload.onPendingSend?.("transcript");
      return Promise.reject(new Error("offline"));
    });
    const { getByTestId, findByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-eager-fail" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "bring me back" } });
    fireEvent.click(getByTestId("composer-send"));
    expect(input.value).toBe("");

    expect((await findByTestId("composer-attach-error")).textContent).toBe(
      "Message could not be sent. Try again.",
    );
    expect(input.value).toBe("bring me back");
    expect(composerDraftForSession("sess-eager-fail")).toBe("bring me back");
  });

  it("does not clobber newer input when an eagerly cleared send fails", async () => {
    let reject: ((err: Error) => void) | undefined;
    const onSend = vi.fn((payload: { onPendingSend?: (destination: "transcript" | "queue") => void }) => {
      payload.onPendingSend?.("transcript");
      return new Promise<boolean | void>((_resolve, rej) => {
        reject = rej;
      });
    });
    const { getByTestId, findByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-eager-newer" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "first ask" } });
    fireEvent.click(getByTestId("composer-send"));
    expect(input.value).toBe("");
    fireEvent.input(input, { target: { value: "a newer thought" } });

    reject?.(new Error("offline"));
    await findByTestId("composer-attach-error");
    expect(input.value).toBe("a newer thought");
  });

  it("sends rapid consecutive prompts without waiting for the first", async () => {
    const settles: Array<(value: boolean | void) => void> = [];
    const onSend = vi.fn((payload: { onPendingSend?: (destination: "transcript" | "queue") => void }) => {
      payload.onPendingSend?.("transcript");
      return new Promise<boolean | void>((resolve) => {
        settles.push(resolve);
      });
    });
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-rapid" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "first" } });
    pressComposerKey(input, { key: "Enter", code: "Enter" });
    expect(input.value).toBe("");
    fireEvent.input(input, { target: { value: "second" } });
    pressComposerKey(input, { key: "Enter", code: "Enter" });

    expect(onSend).toHaveBeenCalledTimes(2);
    expect(onSend).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ text: "first" }),
    );
    expect(onSend).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ text: "second" }),
    );
    for (const settle of settles) settle(undefined);
    await waitFor(() => expect(input.value).toBe(""));
  });

  it("ignores a double submit that lands before the eager clear", async () => {
    let commit: ((destination: "transcript" | "queue") => void) | undefined;
    let settle: ((value: boolean | void) => void) | undefined;
    const onSend = vi.fn((payload: { onPendingSend?: (destination: "transcript" | "queue") => void }) => {
      commit = payload.onPendingSend;
      return new Promise<boolean | void>((resolve) => {
        settle = resolve;
      });
    });
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-double" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "same words" } });
    fireEvent.click(getByTestId("composer-send"));
    pressComposerKey(input, { key: "Enter", code: "Enter" });

    expect(onSend).toHaveBeenCalledTimes(1);
    commit?.("transcript");
    settle?.(undefined);
    await waitFor(() => expect(input.value).toBe(""));
  });

  it("write-through keeps draft across remount and clears only on send", async () => {
    const onSend = vi.fn();
    const first = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-a"
        onSend={onSend}
      />
    ));
    fireEvent.input(first.getByTestId("chat-composer"), {
      target: { value: "keep me" },
    });
    expect(composerDraftForSession("sess-a")).toBe("keep me");
    first.unmount();

    const second = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-a"
        onSend={onSend}
      />
    ));
    expect(
      (second.getByTestId("chat-composer") as HTMLTextAreaElement).value,
    ).toBe("keep me");

    pressComposerKey(second.getByTestId("chat-composer") as HTMLTextAreaElement, {
      key: "Enter",
      code: "Enter",
    });
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({ text: "keep me" }),
    );
    await waitFor(() => expect(composerDraftForSession("sess-a")).toBe(""));
  });

  it("hydrates the matching session draft when sessionId changes", () => {
    setComposerDraft("sess-a", "from a");
    setComposerDraft("sess-b", "from b");
    const [sessionId, setSessionId] = createSignal("sess-a");
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId={sessionId()}
        onSend={vi.fn()}
      />
    ));
    expect((getByTestId("chat-composer") as HTMLTextAreaElement).value).toBe(
      "from a",
    );
    setSessionId("sess-b");
    expect((getByTestId("chat-composer") as HTMLTextAreaElement).value).toBe(
      "from b",
    );
  });

  it("empty submit is ignored", () => {
    const onSend = vi.fn();
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        onSend={onSend}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    pressComposerKey(input, { key: "Enter", code: "Enter" });
    expect(onSend).not.toHaveBeenCalled();
  });
});
