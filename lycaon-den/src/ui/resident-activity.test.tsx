// @vitest-environment jsdom
import { createSignal } from "solid-js";
import { render, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { ResidentPresence } from "./resident-surfaces.ts";
import { ResidentPresenceProvider } from "./resident-presence-context.tsx";
import {
  createResidentActivity,
  createResidentFocus,
} from "./resident-activity.ts";

function Probe(props: { setup: () => () => void }) {
  createResidentActivity(props.setup);
  return null;
}

function FocusProbe(props: {
  target: () => HTMLElement | undefined;
}) {
  createResidentFocus(props.target);
  return null;
}

function RefProbe() {
  let target: HTMLButtonElement | undefined;
  createResidentFocus(() => target);
  return <button ref={target}>Ref target</button>;
}

describe("createResidentActivity", () => {
  it("preserves a later focus choice while activation waits for paint", async () => {
    const target = document.createElement("button");
    const other = document.createElement("button");
    document.body.append(target, other);
    const view = render(() => <FocusProbe target={() => target} />);
    other.focus();
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
    expect(document.activeElement).toBe(other);
    view.unmount();
    target.remove();
    other.remove();
  });

  it("defers focus until paint and cancels an activation that becomes idle", async () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    const target = document.createElement("button");
    document.body.append(target);
    const focus = vi.spyOn(target, "focus");
    const view = render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <FocusProbe target={() => target} />
      </ResidentPresenceProvider>
    ));
    await Promise.resolve();
    expect(focus).not.toHaveBeenCalled();
    setPresence("idle");
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
    expect(focus).not.toHaveBeenCalled();
    setPresence("active");
    await waitFor(() => expect(document.activeElement).toBe(target));
    view.unmount();
    target.remove();
  });

  it("releases idle work and reacquires it without remounting", () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    const cleanup = vi.fn();
    const setup = vi.fn(() => cleanup);
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <Probe setup={setup} />
      </ResidentPresenceProvider>
    ));

    expect(setup).toHaveBeenCalledTimes(1);
    setPresence("idle");
    expect(cleanup).toHaveBeenCalledTimes(1);
    setPresence("pending");
    expect(setup).toHaveBeenCalledTimes(2);
  });

  it("is active outside a resident stage", () => {
    const setup = vi.fn();
    render(() => <Probe setup={() => { setup(); return () => {}; }} />);
    expect(setup).toHaveBeenCalledTimes(1);
  });

  it("keeps acquisition running when a prepared surface becomes visible", () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("pending");
    const cleanup = vi.fn();
    const setup = vi.fn(() => cleanup);
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <Probe setup={setup} />
      </ResidentPresenceProvider>
    ));

    setPresence("active");
    expect(setup).toHaveBeenCalledTimes(1);
    expect(cleanup).not.toHaveBeenCalled();
    setPresence("idle");
    expect(cleanup).toHaveBeenCalledTimes(1);
  });

  it("focuses a late task target on every reveal", async () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");
    const [target, setTarget] = createSignal<HTMLButtonElement>();
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <FocusProbe target={target} />
        <button ref={setTarget}>Task target</button>
      </ResidentPresenceProvider>
    ));

    expect(document.activeElement).not.toBe(target());
    setPresence("pending");
    await Promise.resolve();
    expect(document.activeElement).not.toBe(target());

    setPresence("active");
    await waitFor(() => expect(document.activeElement).toBe(target()));

    setPresence("idle");
    target()?.blur();
    setPresence("active");
    await waitFor(() => expect(document.activeElement).toBe(target()));
  });

  it("resolves a non-reactive child ref after the activation effect", async () => {
    render(() => (
      <ResidentPresenceProvider presence="active">
        <RefProbe />
      </ResidentPresenceProvider>
    ));

    await waitFor(() => {
      expect(document.activeElement?.textContent).toBe("Ref target");
    });
  });

  it("retries after an outgoing inert claim releases", async () => {
    const shell = document.createElement("div");
    document.body.append(shell);
    const target = document.createElement("button");
    target.disabled = true;
    shell.append(target);

    render(() => (
      <ResidentPresenceProvider presence="active">
        <FocusProbe target={() => target} />
      </ResidentPresenceProvider>
    ));
    await Promise.resolve();
    expect(document.activeElement).not.toBe(target);

    target.disabled = false;
    await waitFor(() => expect(document.activeElement).toBe(target));
    shell.remove();
  });

  it("focuses when a pending stage releases its inert subtree", async () => {
    // A disabled fieldset models focus blocking until the ancestor releases it.
    const shell = document.createElement("fieldset");
    shell.disabled = true;
    document.body.append(shell);
    const target = document.createElement("button");
    shell.append(target);

    render(() => (
      <ResidentPresenceProvider presence="active">
        <FocusProbe target={() => target} />
      </ResidentPresenceProvider>
    ));
    await Promise.resolve();
    expect(document.activeElement).not.toBe(target);

    shell.disabled = false;
    await waitFor(() => expect(document.activeElement).toBe(target));
    shell.remove();
  });

  it("repairs focus lost to the document during the activating turn", async () => {
    const target = document.createElement("button");
    document.body.append(target);
    render(() => (
      <ResidentPresenceProvider presence="active">
        <FocusProbe target={() => target} />
      </ResidentPresenceProvider>
    ));

    await waitFor(() => expect(document.activeElement).toBe(target));
    target.blur();
    await waitFor(() => expect(document.activeElement).toBe(target));
    target.remove();
  });
});
