import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { SessionMode } from "../../api/types.ts";
import { ComposerChromeSlot } from "./ComposerChromeSlot.tsx";
import { SessionModeSwitch } from "./SessionModeSwitch.tsx";

describe("Session mode switch", () => {
  it("exposes two radios with one tab stop and arrow-key selection", () => {
    const [mode, setMode] = createSignal<SessionMode>("agent");
    render(() => <SessionModeSwitch mode={mode()} onSelect={setMode} />);
    const group = screen.getByRole("radiogroup", { name: "Session mode" });
    const agent = screen.getByRole("radio", { name: "Agent" });
    const ask = screen.getByRole("radio", { name: "Ask" });
    expect(group.getAttribute("aria-describedby")).toBeTruthy();
    expect(screen.getAllByText(/read-only declaration/)).toHaveLength(1);
    expect(agent.tabIndex).toBe(0);
    expect(ask.tabIndex).toBe(-1);

    agent.focus();
    fireEvent.keyDown(agent, { key: "ArrowRight" });
    expect(document.activeElement).toBe(ask);
    expect(ask.getAttribute("aria-checked")).toBe("true");
    expect(ask.tabIndex).toBe(0);
    fireEvent.keyDown(ask, { key: "Home" });
    expect(document.activeElement).toBe(agent);
    expect(agent.getAttribute("aria-checked")).toBe("true");
  });

  it("retains confirmed Ask during upgrade and permits retry after uncertainty", () => {
    const [phase, setPhase] = createSignal<"sending" | "reconciling">("sending");
    const select = vi.fn();
    render(() => <SessionModeSwitch started mode="ask" upgrade={phase()} onSelect={select} />);
    const agent = screen.getByRole("radio", { name: "Agent" });
    const ask = screen.getByRole("radio", { name: "Ask" });
    expect(screen.getByRole("radiogroup").getAttribute("aria-busy")).toBe("true");
    expect(ask.getAttribute("aria-checked")).toBe("true");
    expect(agent.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(agent);
    expect(select).not.toHaveBeenCalled();

    setPhase("reconciling");
    fireEvent.click(ask);
    expect(select).not.toHaveBeenCalled();
    fireEvent.click(agent);
    expect(select).toHaveBeenCalledWith("agent");
    expect(screen.getByRole("status").textContent).toContain("Select Agent to retry");
  });

  it.each([true, false])("restores focus from the fading slot only when it owns focus (%s)", (inside) => {
    const [present, setPresent] = createSignal(true);
    const focusExit = vi.fn(() => screen.getByRole("textbox").focus());
    render(() => (
      <>
        <ComposerChromeSlot slot="mode" motion="fade" present={present()} onFocusExit={focusExit}>
          <SessionModeSwitch mode="ask" started onSelect={() => {}} />
        </ComposerChromeSlot>
        <input aria-label="Composer" />
        <button type="button">Elsewhere</button>
      </>
    ));
    const focus = inside ? screen.getByRole("radio", { name: "Agent" }) : screen.getByRole("button", { name: "Elsewhere" });
    focus.focus();
    setPresent(false);
    expect(screen.queryByRole("radiogroup")).toBeNull();
    expect(focusExit.mock.calls).toHaveLength(inside ? 1 : 0);
    expect(document.activeElement).toBe(inside ? screen.getByRole("textbox") : focus);
  });
});
