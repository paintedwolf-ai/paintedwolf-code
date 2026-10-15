import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { createRoot, createSignal } from "solid-js";
import { bindScrollportMotion, scrollportMotionForHost, unbindScrollportMotion } from "../../../platform/scrolling/scrollport-motion.ts";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../../stream/transcript-viewport.tsx";
import {
  TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT,
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  TranscriptDisclosureToggle,
  useTranscriptDisclosure,
} from "./transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "./transcript-disclosure-key.ts";

const ROW = transcriptDisclosureKey.tool("row-1");

function Disclosure() {
  const disclosure = useTranscriptDisclosure(() => ROW);
  return (
    <TranscriptDisclosureShell disclosure={disclosure} data-testid="shell">
      <TranscriptDisclosureToggle disclosure={disclosure}>Details</TranscriptDisclosureToggle>
      <TranscriptDisclosurePanel disclosure={disclosure}>Body</TranscriptDisclosurePanel>
    </TranscriptDisclosureShell>
  );
}

/** A disclosure assembled by hand, so motion runs against stubbed geometry. */
function manualDisclosure() {
  const disclosure = useTranscriptDisclosure();
  const shell = document.createElement("div");
  shell.setAttribute("data-transcript-disclosure", "");
  const toggle = document.createElement("button");
  const body = document.createElement("div");
  shell.append(toggle, body);
  document.body.append(shell);
  Object.defineProperty(shell, "scrollHeight", { value: 180 });
  shell.getBoundingClientRect = () => ({ height: disclosure.open() ? 180 : 28 }) as DOMRect;
  toggle.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
  const animations: Animation[] = [];
  shell.animate = vi.fn(() => {
    const animation = { cancel: vi.fn(), onfinish: null } as unknown as Animation;
    animations.push(animation);
    return animation;
  });
  const click = () => disclosure.onToggleClick({ currentTarget: toggle, target: toggle } as unknown as
    MouseEvent & { currentTarget: HTMLElement; target: Element });
  return { disclosure, shell, body, animations, click };
}

describe("transcript disclosure leases", () => {
  it.each([true, false])("opening a bound disclosure holds the reader (animated=%s)", async (animated) => {
    const viewport = createTranscriptViewportController({ sessionId: () => "s-bound" });
    const view = render(() => (
      <TranscriptViewportProvider value={viewport}><Disclosure /></TranscriptViewportProvider>
    ));
    const host = view.container;
    const details = view.getByTestId("shell");
    const summary = view.getByRole("button");
    let height = 300;
    Object.defineProperties(host, {
      clientHeight: { get: () => 300 },
      scrollHeight: { get: () => animated ? height : details.getAttribute("data-expanded") === "true" ? 900 : 300 },
    });
    host.getBoundingClientRect = () => ({ top: 0, bottom: 300, height: 300 }) as DOMRect;
    bindScrollportMotion(host, host, host);
    viewport.attachStream(host);
    const motion = scrollportMotionForHost(host)!;
    const animation = { cancel: vi.fn(), onfinish: null } as unknown as Animation;
    if (animated) {
      Object.defineProperty(details, "scrollHeight", { value: 700 });
      details.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
      summary.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
      details.animate = vi.fn(() => animation);
    }
    try {
      fireEvent.click(summary);
      height = 900;
      motion.notifyLayoutMutated();
      expect(host.scrollTop).toBe(0);
      if (animated) {
        animation.onfinish?.call(animation, new Event("finish") as AnimationPlaybackEvent);
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
      }
      expect(viewport.following()).toBe(false);
      motion.notifyLayoutMutated();
      expect(host.scrollTop).toBe(0);
    } finally {
      viewport.attachStream(null);
      unbindScrollportMotion(host);
    }
  });

  it("keeps following through the animated disclosure path", () => {
    const viewport = createTranscriptViewportController({ sessionId: () => "s1" });
    const view = render(() => (
      <TranscriptViewportProvider value={viewport}>
        <Disclosure />
      </TranscriptViewportProvider>
    ));
    const details = view.getByTestId("shell");
    const summary = view.getByRole("button");
    Object.defineProperty(details, "scrollHeight", { value: 180 });
    details.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
    summary.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
    const animation = { cancel: vi.fn(), onfinish: null } as unknown as Animation;
    details.animate = vi.fn(() => animation);

    fireEvent.click(summary);
    expect(details.animate).toHaveBeenCalledOnce();
    expect(viewport.following()).toBe(true);
    animation.onfinish?.call(animation, new Event("finish") as AnimationPlaybackEvent);
    expect(viewport.following()).toBe(true);
  });

  it.each([true, false])("preserves following=%s when opening and closing a transcript disclosure", (following) => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    if (!following) controller.stopFollowing();
    const view = render(() => (
      <TranscriptViewportProvider value={controller}>
        <Disclosure />
      </TranscriptViewportProvider>
    ));

    fireEvent.click(view.getByText("Details"));

    expect(controller.following()).toBe(following);
    expect(controller.disclosures.isOpen(ROW)).toBe(true);
    fireEvent.click(view.getByText("Details"));
    expect(controller.following()).toBe(following);
    expect(controller.disclosures.isOpen(ROW)).toBe(false);
  });
  it("keeps human state across keyed remounts", () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const first = render(() => (
      <TranscriptViewportProvider value={controller}>
        <Disclosure />
      </TranscriptViewportProvider>
    ));
    fireEvent.click(first.getByText("Details"));
    expect(controller.disclosures.isOpen(ROW)).toBe(true);
    first.unmount();

    const second = render(() => (
      <TranscriptViewportProvider value={controller}>
        <Disclosure />
      </TranscriptViewportProvider>
    ));
    expect(second.getByTestId("shell").getAttribute("data-expanded") === "true").toBe(true);
  });

  it("exposes the toggle as a button that controls its panel", () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const view = render(() => (
      <TranscriptViewportProvider value={controller}>
        <Disclosure />
      </TranscriptViewportProvider>
    ));
    const toggle = view.getByRole("button", { name: "Details" });
    const panel = view.getByText("Body");
    expect(toggle.getAttribute("type")).toBe("button");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-controls")).toBe(panel.id);
    expect(toggle.classList.contains("den-transcript-disclosure-header")).toBe(true);
    expect(panel.getAttribute("aria-hidden")).toBe("true");
    expect(panel.inert).toBeTruthy();
    const layouts = vi.fn();
    view.getByTestId("shell").addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, layouts);
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(panel.hasAttribute("aria-hidden")).toBe(false);
    expect(panel.inert).toBeFalsy();
    expect(view.getByTestId("shell").getAttribute("data-expanded") === "true").toBe(true);
    expect(layouts).toHaveBeenCalled();
  });

  it("lets a toggle beside other header controls and custom click handlers drive the card", () => {
    const onClick = vi.fn();
    const view = render(() => {
      const disclosure = useTranscriptDisclosure();
      return (
        <TranscriptDisclosureShell disclosure={disclosure} data-testid="shell">
          <div data-testid="header">
            <TranscriptDisclosureToggle disclosure={disclosure} inHeader>Open</TranscriptDisclosureToggle>
            <TranscriptDisclosureToggle disclosure={disclosure} inHeader onClick={onClick}>Custom</TranscriptDisclosureToggle>
          </div>
          <TranscriptDisclosurePanel disclosure={disclosure}>Body</TranscriptDisclosurePanel>
        </TranscriptDisclosureShell>
      );
    });
    const toggle = view.getByRole("button", { name: "Open" });
    expect(toggle.classList.contains("den-transcript-disclosure-header")).toBe(false);
    fireEvent.click(toggle);
    expect(view.getByTestId("shell").getAttribute("data-expanded") === "true").toBe(true);
    fireEvent.click(view.getByRole("button", { name: "Custom" }));
    expect(onClick).toHaveBeenCalledOnce();
    expect(view.getByTestId("shell").getAttribute("data-expanded") === "true").toBe(true);
  });

  it("ignores a toggle outside any disclosure card", () => {
    createRoot((dispose) => {
      const disclosure = useTranscriptDisclosure();
      const toggle = document.createElement("button");
      disclosure.onToggleClick({ currentTarget: toggle, target: toggle } as unknown as
        MouseEvent & { currentTarget: HTMLElement; target: Element });
      expect(disclosure.open()).toBe(false);
      dispose();
    });
  });

  it("layers temporary leases without mutating human state", () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const releaseFind = controller.disclosures.acquire(ROW);
    const releaseNavigation = controller.disclosures.acquire(ROW);
    expect(controller.disclosures.isOpen(ROW)).toBe(true);
    expect(controller.disclosures.userOpenKeys()).toEqual([]);
    releaseFind();
    expect(controller.disclosures.isOpen(ROW)).toBe(true);
    releaseNavigation();
    expect(controller.disclosures.isOpen(ROW)).toBe(false);
  });

  it("lets direct human intent supersede temporary leases", () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const release = controller.disclosures.acquire(ROW);
    controller.disclosures.setUserOpen(ROW, true);
    release();
    expect(controller.disclosures.userOpenKeys()).toEqual([ROW]);
    expect(controller.disclosures.isOpen(ROW)).toBe(true);
  });

  it("animates measured open and close heights without dropping the body early", async () => {
    await createRoot(async (dispose) => {
      const { disclosure, shell, body, animations, click } = manualDisclosure();
      const layouts = vi.fn();
      shell.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, layouts);

      click();
      expect(layouts).toHaveBeenCalledTimes(1);
      expect(disclosure.open()).toBe(true);
      expect(shell.style.height).toBe("28px");
      expect(shell.hasAttribute("data-animating")).toBe(true);
      expect(shell.animate).toHaveBeenLastCalledWith(
        { height: ["28px", "180px"] },
        expect.objectContaining({ duration: 240, fill: "forwards" }),
      );
      animations[0]!.onfinish?.call(animations[0]!, new Event("finish") as AnimationPlaybackEvent);
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(layouts).toHaveBeenCalledTimes(2);

      click();
      expect(layouts).toHaveBeenCalledTimes(3);
      expect(disclosure.open()).toBe(true);
      expect(shell.dataset.closing).toBe("true");
      expect(shell.animate).toHaveBeenLastCalledWith(
        { height: ["180px", "28px"] },
        expect.objectContaining({ duration: 240, fill: "forwards" }),
      );
      animations[1]!.onfinish?.call(animations[1]!, new Event("finish") as AnimationPlaybackEvent);
      expect(disclosure.open()).toBe(false);
      expect(body.style.display).toBe("none");
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(layouts).toHaveBeenCalledTimes(4);
      expect(shell.style.height).toBe("");
      expect(body.style.display).toBe("");
      expect(shell.hasAttribute("data-animating")).toBe(false);
      shell.remove();
      dispose();
    });
  });

  it("reverses an in-flight close instead of swallowing the second click", async () => {
    await createRoot(async (dispose) => {
      const { shell, animations, click } = manualDisclosure();
      click();
      animations[0]!.onfinish?.call(animations[0]!, new Event("finish") as AnimationPlaybackEvent);
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      click();
      click();
      expect(animations).toHaveLength(3);
      expect(animations[1]!.cancel).toHaveBeenCalledOnce();
      expect(shell.dataset.closing).toBeUndefined();
      shell.remove();
      dispose();
    });
  });

  it("tracks dynamic key changes reactively", () => {
    createRoot((dispose) => {
      const [key, setKey] = createSignal("initial");
      const disclosure = useTranscriptDisclosure(() => transcriptDisclosureKey.tool(key()));
      expect(disclosure.key).toBe(transcriptDisclosureKey.tool("initial"));
      setKey("updated");
      expect(disclosure.key).toBe(transcriptDisclosureKey.tool("updated"));
      dispose();
    });
  });
});
