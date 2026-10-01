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
  useTranscriptDisclosure,
} from "./transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "./transcript-disclosure-key.ts";

const ROW = transcriptDisclosureKey.tool("row-1");

function Disclosure() {
  const disclosure = useTranscriptDisclosure(() => ROW);
  return (
    <details open={disclosure.open()} onToggle={disclosure.onToggle}>
      <summary onClick={disclosure.onSummaryClick}>Details</summary>
      <p>Body</p>
    </details>
  );
}

describe("transcript disclosure leases", () => {
  it.each([true, false])("opening a bound disclosure holds the reader (animated=%s)", async (animated) => {
    const viewport = createTranscriptViewportController({ sessionId: () => "s-bound" });
    const view = render(() => (
      <TranscriptViewportProvider value={viewport}><Disclosure /></TranscriptViewportProvider>
    ));
    const host = view.container;
    const details = host.querySelector("details")!;
    const summary = details.querySelector("summary")!;
    let height = 300;
    Object.defineProperties(host, {
      clientHeight: { get: () => 300 },
      scrollHeight: { get: () => animated ? height : details.open ? 900 : 300 },
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
    const details = view.container.querySelector("details")!;
    const summary = details.querySelector("summary")!;
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
    expect((second.container.querySelector("details") as HTMLDetailsElement).open).toBe(true);
  });

  it("does not publish layout when a controlled open disclosure remounts", () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    controller.disclosures.setUserOpen(ROW, true);
    const view = render(() => (
      <TranscriptViewportProvider value={controller}>
        <Disclosure />
      </TranscriptViewportProvider>
    ));
    const details = view.container.querySelector("details") as HTMLDetailsElement;
    const layouts = vi.fn();
    details.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, layouts);

    details.dispatchEvent(new Event("toggle"));
    expect(layouts).not.toHaveBeenCalled();

    details.open = false;
    details.dispatchEvent(new Event("toggle"));
    expect(layouts).toHaveBeenCalledOnce();
    expect(controller.disclosures.isOpen(ROW)).toBe(false);
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
      const disclosure = useTranscriptDisclosure();
      const details = document.createElement("details");
      const summary = document.createElement("summary");
      const body = document.createElement("div");
      details.append(summary, body);
      document.body.append(details);
      Object.defineProperty(details, "scrollHeight", { value: 180 });
      details.getBoundingClientRect = () =>
        ({ height: details.open ? 180 : 28 }) as DOMRect;
      summary.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
      const layouts = vi.fn();
      details.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, layouts);

      const animations: Animation[] = [];
      details.animate = vi.fn(() => {
        const animation = {
          cancel: vi.fn(),
          onfinish: null,
        } as unknown as Animation;
        animations.push(animation);
        return animation;
      });
      const click = () =>
        disclosure.onSummaryClick({
          currentTarget: summary,
          target: summary,
          preventDefault: vi.fn(),
        } as unknown as MouseEvent & {
          currentTarget: HTMLElement;
          target: Element;
        });

      click();
      expect(layouts).toHaveBeenCalledTimes(1);
      expect(details.open).toBe(true);
      expect(details.style.height).toBe("28px");
      expect(details.hasAttribute("data-animating")).toBe(true);
      expect(details.animate).toHaveBeenLastCalledWith(
        { height: ["28px", "180px"] },
        expect.objectContaining({ duration: 240, fill: "forwards" }),
      );
      animations[0]!.onfinish?.call(
        animations[0]!,
        new Event("finish") as AnimationPlaybackEvent,
      );
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(layouts).toHaveBeenCalledTimes(2);

      click();
      expect(layouts).toHaveBeenCalledTimes(3);
      expect(details.open).toBe(true);
      expect(details.dataset.closing).toBe("true");
      expect(details.animate).toHaveBeenLastCalledWith(
        { height: ["180px", "28px"] },
        expect.objectContaining({ duration: 240, fill: "forwards" }),
      );
      animations[1]!.onfinish?.call(
        animations[1]!,
        new Event("finish") as AnimationPlaybackEvent,
      );
      expect(details.open).toBe(false);
      expect(body.style.display).toBe("none");
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(layouts).toHaveBeenCalledTimes(4);
      expect(details.style.height).toBe("");
      expect(body.style.display).toBe("");
      expect(details.hasAttribute("data-animating")).toBe(false);
      details.remove();
      dispose();
    });
  });

  it("reverses an in-flight close instead of swallowing the second click", async () => {
    await createRoot(async (dispose) => {
      const disclosure = useTranscriptDisclosure();
      const details = document.createElement("details");
      const summary = document.createElement("summary");
      details.append(summary, document.createElement("div"));
      document.body.append(details);
      Object.defineProperty(details, "scrollHeight", { value: 180 });
      details.getBoundingClientRect = () => ({ height: details.open ? 180 : 28 }) as DOMRect;
      summary.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
      const animations: Animation[] = [];
      details.animate = vi.fn(() => {
        const animation = { cancel: vi.fn(), onfinish: null } as unknown as Animation;
        animations.push(animation);
        return animation;
      });
      const click = () => disclosure.onSummaryClick({
        currentTarget: summary,
        target: summary,
        preventDefault: vi.fn(),
      } as unknown as MouseEvent & { currentTarget: HTMLElement; target: Element });

      click();
      animations[0]!.onfinish?.call(
        animations[0]!,
        new Event("finish") as AnimationPlaybackEvent,
      );
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      click();
      click();
      expect(animations).toHaveLength(3);
      expect(animations[1]!.cancel).toHaveBeenCalledOnce();
      expect(details.dataset.closing).toBeUndefined();
      details.remove();
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
