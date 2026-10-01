import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setThemeGlyphs } from "../../contributions/theme-icons.ts";
import { TooltipHost } from "../../components/primitives/TooltipHost.tsx";
import { FilesTabKindIcon } from "./FilesTab.tsx";
import { fileTabKind } from "./files-tab-kind.ts";
import type { FileBufferKind } from "../documents/project-files-buffer-kind.ts";

afterEach(() => {
  cleanup();
  setThemeGlyphs({});
  vi.useRealTimers();
});

describe("special tab icons", () => {
  it.each(["text", "image", "info"] as const)("keeps %s current file views unmarked", (kind) => {
    const { container } = render(() => <FilesTabKindIcon buffer={{ kind }} />);
    expect(container.childElementCount).toBe(0);
  });

  it.each([
    [{ kind: "walk" }, "file-group", "Group summary"],
    [{ kind: "diffs" }, "diff", "All diffs"],
    [{ kind: "chat" }, "session", "Chat content"],
    [{ kind: "chat", chatContent: { kind: "tool" } }, "tool", "Tool call details"],
    [{ kind: "chat", chatContent: { kind: "approval" } }, "settings-approvals", "Approval details"],
    [{ kind: "trust" }, "shield", "Trust changes"],
    [{ kind: "diff" }, "rewind", "File comparison"],
  ] as const)("uses the themed %s mark with an icon-only tooltip", async (buffer, slot, label) => {
    vi.useFakeTimers();
    setThemeGlyphs({ [slot]: [{ tag: "circle", attrs: { cx: "8", cy: "8", r: "4" } }] });
    const activate = vi.fn();
    render(() => <>
      <button type="button" role="tab" aria-label={`Example, ${fileTabKind(buffer)!.label}`} onClick={activate}>
        <FilesTabKindIcon buffer={buffer} /><span>Example</span>
      </button>
      <TooltipHost />
    </>);
    const tab = screen.getByRole("tab", { name: `Example, ${label}` });
    const icon = tab.querySelector<HTMLElement>(".den-files-tab-kind-icon")!;
    expect(icon.querySelector("circle")?.getAttribute("r")).toBe("4");
    expect(icon.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(icon.querySelector("svg")?.getAttribute("width")).toBe("14");
    expect(tab.querySelectorAll("button, [tabindex]")).toHaveLength(0);
    fireEvent.mouseOver(tab);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.queryByRole("tooltip")).toBeNull();
    fireEvent.mouseOver(icon);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").textContent).toBe(label);
    fireEvent.click(icon);
    expect(activate).toHaveBeenCalledOnce();
    fireEvent.mouseOut(icon, { relatedTarget: tab });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("updates identity when a tab changes kind", () => {
    const [kind, setKind] = createSignal<FileBufferKind>("chat");
    const { container } = render(() => <FilesTabKindIcon buffer={{ kind: kind() }} />);
    expect(container.querySelector("[data-tip]")?.getAttribute("data-tip")).toBe("Chat content");
    setKind("trust");
    expect(container.querySelector("[data-tip]")?.getAttribute("data-tip")).toBe("Trust changes");
    setKind("text");
    expect(container.childElementCount).toBe(0);
  });

  it("distinguishes tool and approval content inside the shared chat tab kind", () => {
    const [kind, setKind] = createSignal<"tool" | "approval">("tool");
    const { container } = render(() => <FilesTabKindIcon buffer={{ kind: "chat", chatContent: { kind: kind() } }} />);
    expect(container.querySelector("[data-tip]")?.getAttribute("data-tip")).toBe("Tool call details");
    setKind("approval");
    expect(container.querySelector("[data-tip]")?.getAttribute("data-tip")).toBe("Approval details");
  });

  it.each(["text", "image", "info"] as const)("marks a previous %s version and clears it on return to current", (kind) => {
    const [previous, setPrevious] = createSignal(true);
    const { container } = render(() => <FilesTabKindIcon buffer={{ kind }} previousVersion={previous()} />);
    expect(container.querySelector("[data-tip]")?.getAttribute("data-tip")).toBe("Previous version");
    setPrevious(false);
    expect(container.childElementCount).toBe(0);
  });
});
