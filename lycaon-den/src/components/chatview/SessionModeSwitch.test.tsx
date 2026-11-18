import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { SessionMode } from "../../api/types.ts";
import { SessionModeSwitch } from "./SessionModeSwitch.tsx";

describe("Session mode switch", () => {
  it("moves focus and the single tab stop together and describes each mode", () => {
    const [mode, setMode] = createSignal<SessionMode>("agent");
    render(() => <SessionModeSwitch mode={mode()} onSelect={setMode} />);
    const group = screen.getByRole("radiogroup", { name: "Session mode" });
    const agent = screen.getByRole("radio", { name: "Agent" });
    const ask = screen.getByRole("radio", { name: "Ask" });
    expect(group.classList.contains("den-browse-segmented")).toBe(true);
    expect(document.getElementById(agent.getAttribute("aria-describedby")!)?.textContent)
      .toBe("Can edit files and run commands.");
    expect(document.getElementById(ask.getAttribute("aria-describedby")!)?.textContent)
      .toBe("Reads and answers. Can’t edit files or run commands.");
    expect(agent.hasAttribute("data-tip")).toBe(false);
    expect(ask.hasAttribute("data-tip")).toBe(false);
    expect(agent.tabIndex).toBe(0);
    expect(ask.tabIndex).toBe(-1);

    agent.focus();
    fireEvent.keyDown(agent, { key: "ArrowRight" });
    expect(document.activeElement).toBe(ask);
    expect(ask.getAttribute("aria-checked")).toBe("true");
    expect(ask.tabIndex).toBe(0);
    expect(agent.tabIndex).toBe(-1);

    fireEvent.keyDown(ask, { key: "Home" });
    expect(document.activeElement).toBe(agent);
    fireEvent.click(ask);
    expect(ask.getAttribute("aria-checked")).toBe("true");
  });

  it("keeps Ask checked and offers a permanent Agent upgrade on a started chat", () => {
    const select = vi.fn();
    render(() => <SessionModeSwitch started mode="ask" onSelect={select} />);
    const agent = screen.getByRole("radio", { name: "Agent" });
    const ask = screen.getByRole("radio", { name: "Ask" });
    expect(document.getElementById(agent.getAttribute("aria-describedby")!)?.textContent)
      .toBe("Switches this chat to Agent. It can’t be switched back.");
    expect(ask.getAttribute("aria-checked")).toBe("true");
    expect(ask.tabIndex).toBe(0);

    fireEvent.click(ask);
    expect(select).not.toHaveBeenCalled();
    fireEvent.keyDown(ask, { key: "ArrowRight" });
    expect(document.activeElement).toBe(agent);
    expect(select).toHaveBeenCalledExactlyOnceWith("agent");
  });

  it("reports busy without status text and allows reselecting Agent after uncertainty", () => {
    const [phase, setPhase] = createSignal<"sending" | "reconciling">("sending");
    const select = vi.fn();
    render(() => <SessionModeSwitch started mode="agent" upgrade={phase()} onSelect={select} />);
    const group = screen.getByRole("radiogroup");
    const agent = screen.getByRole("radio", { name: "Agent" });
    const ask = screen.getByRole("radio", { name: "Ask" });
    expect(group.getAttribute("aria-busy")).toBe("true");
    expect(agent.getAttribute("aria-checked")).toBe("true");
    expect(agent.getAttribute("aria-disabled")).toBe("true");
    expect(screen.queryByRole("status")).toBeNull();
    fireEvent.click(agent);
    fireEvent.click(ask);
    expect(select).not.toHaveBeenCalled();

    setPhase("reconciling");
    expect(group.hasAttribute("aria-busy")).toBe(false);
    fireEvent.click(ask);
    expect(select).not.toHaveBeenCalled();
    fireEvent.click(agent);
    expect(select).toHaveBeenCalledExactlyOnceWith("agent");
  });

  it("ignores pointer and keyboard choices while disabled", () => {
    const select = vi.fn();
    render(() => <SessionModeSwitch disabled mode="agent" onSelect={select} />);
    const agent = screen.getByRole("radio", { name: "Agent" });
    const ask = screen.getByRole("radio", { name: "Ask" });
    fireEvent.click(ask);
    fireEvent.keyDown(agent, { key: "ArrowRight" });
    expect(select).not.toHaveBeenCalled();
    expect(agent.tabIndex).toBe(0);
  });
});
