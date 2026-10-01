// @vitest-environment jsdom
import { render, waitFor } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { afterEach, describe, expect, it } from "vitest";
import {
  resetDispatcherForTests,
  shortcutCommandsSuspended,
} from "../../shortcuts/dispatcher.ts";
import {
  createAnchoredPopoverFocus,
  createModalFocusTrap,
  createOverlayScopeFocusTrap,
} from "./modal-focus-trap.ts";

afterEach(() => {
  resetDispatcherForTests();
  document.body.replaceChildren();
});

describe("modal focus traps", () => {
  it("cancels queued popover focus after the popover closes", async () => {
    const [open, setOpen] = createSignal(true);

    function Harness() {
      let trigger: HTMLButtonElement | undefined;
      let panel: HTMLDivElement | undefined;
      createAnchoredPopoverFocus(open, () => panel, {
        trigger: () => trigger,
        onEscape: () => setOpen(false),
      });
      return (
        <>
          <button ref={trigger}>Open</button>
          <div ref={panel}><button>Popover control</button></div>
          <button>Destination</button>
        </>
      );
    }

    const view = render(() => <Harness />);
    setOpen(false);
    const destination = view.getByRole("button", { name: "Destination" });
    destination.focus();
    await Promise.resolve();

    expect(document.activeElement).toBe(destination);
  });

  it("controls and releases the app shortcut boundary with the modal", async () => {
    const [open, setOpen] = createSignal(true);

    function Harness() {
      let dialog: HTMLDivElement | undefined;
      createModalFocusTrap(open, () => dialog);
      return (
        <Show when={open()}>
          <div ref={dialog} role="dialog" aria-modal="true">
            <button>Close</button>
          </div>
        </Show>
      );
    }

    render(() => <Harness />);
    await waitFor(() => expect(shortcutCommandsSuspended()).toBe(true));
    setOpen(false);
    await waitFor(() => expect(shortcutCommandsSuspended()).toBe(false));
  });

  it("releases a retained modal's shortcut and focus claims without closing it", async () => {
    const [interactive, setInteractive] = createSignal(true);
    function Harness() {
      let dialog: HTMLDivElement | undefined;
      createModalFocusTrap(() => true, () => dialog);
      return <div ref={dialog} role="dialog"><button>Modal control</button></div>;
    }
    const view = render(() => <>
      <button>Destination</button>
      <ResidentPresenceProvider presence="active" interactive={interactive()}><Harness /></ResidentPresenceProvider>
    </>);
    await waitFor(() => expect(shortcutCommandsSuspended()).toBe(true));
    const dialog = view.getByRole("dialog");
    setInteractive(false);
    expect(shortcutCommandsSuspended()).toBe(false);
    const destination = view.getByRole("button", { name: "Destination" });
    destination.focus();
    await Promise.resolve();
    expect(document.activeElement).toBe(destination);
    setInteractive(true);
    await waitFor(() => expect(shortcutCommandsSuspended()).toBe(true));
    expect(view.getByRole("dialog")).toBe(dialog);
  });

  it("keeps overlay commands outside the modal suspension boundary", async () => {
    function Harness() {
      let dialog: HTMLDivElement | undefined;
      createOverlayScopeFocusTrap(() => true, () => dialog);
      return (
        <div ref={dialog} role="dialog" aria-modal="true">
          <button>Close</button>
        </div>
      );
    }

    render(() => <Harness />);
    await Promise.resolve();
    expect(shortcutCommandsSuspended()).toBe(false);
  });
});
