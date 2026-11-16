import { createEffect, createSignal } from "solid-js";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CODE_FOLD_LINE_THRESHOLD,
  CODE_FOLD_VISIBLE_LINES,
  countFenceLines,
  createMarkdownCodeFoldController,
} from "./code-fold.ts";
import { renderMarkdownHtml } from "./markdown-render.ts";
import { AssistantChatTurn } from "../../components/transcript/AssistantChatTurn.tsx";
import { AssistantProseBody } from "../../components/transcript/AssistantProseBody.tsx";
import { MarkdownBody } from "../../components/transcript/MarkdownBody.tsx";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../stream/transcript-viewport.tsx";
import {
  TranscriptDisclosureProvider,
  createTranscriptDisclosureStore,
} from "../transcript/presentation/disclosure-state.tsx";
import { transcriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../transcript/presentation/transcript-disclosure.tsx";
import {
  closeFind,
  findController,
  findNext,
  findPrev,
  openFind,
  registerFindableView,
  resetFindControllerForTests,
  setFindQuery,
  setPrimaryFindableView,
} from "../../find/find-controller.ts";
import { tryConsumeEscapeLadder } from "../../find/escape-ladder.ts";
import { collapsedRevealHostsContaining } from "../../find/find-reveal.ts";
import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";
import { scrollportFrameParts } from "../../platform/scrolling/scrollport-frame-dom.ts";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";
import { assertNamedControl } from "../../test/controls.ts";
import { mockScrollerMotion } from "../../test/scroll-mock.ts";
import * as clipboard from "../../utils/clipboard.ts";

function buildLines(count: number, prefix = "line"): string {
  const out: string[] = [];
  for (let i = 1; i <= count; i += 1) {
    out.push(`${prefix}-${i}`);
  }
  return out.join("\n");
}

function buildFencedMarkdown(lineCount: number, lang = "ts", prefix = "line"): string {
  return `\`\`\`${lang}\n${buildLines(lineCount, prefix)}\n\`\`\``;
}

const flushMicrotasks = () => new Promise<void>((resolve) => queueMicrotask(resolve));
const nextFrame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));

describe("code-fold", () => {
  afterEach(() => {
    resetFindControllerForTests();
    vi.restoreAllMocks();
  });

  it("counts lines across 1..500 after removing at most one trailing newline", () => {
    expect(CODE_FOLD_LINE_THRESHOLD).toBe(24);
    expect(CODE_FOLD_VISIBLE_LINES).toBe(12);
    expect(countFenceLines("")).toBe(0);
    expect(countFenceLines("\n")).toBe(0);
    expect(countFenceLines("\r\n")).toBe(0);

    for (let n = 1; n <= 500; n += 1) {
      const body = buildLines(n);
      expect(countFenceLines(body)).toBe(n);
      expect(countFenceLines(`${body}\n`)).toBe(n);
      expect(countFenceLines(`${body}\r\n`)).toBe(n);
      expect(countFenceLines(`${body}\n\n`)).toBe(n + 1);
    }
  });

  it("folds iff line count exceeds 24 across heights 1..500 while keeping DOM text intact", () => {
    const root = document.createElement("div");
    root.className = "markdown-body";
    document.body.appendChild(root);
    const fold = createMarkdownCodeFoldController();

    try {
      for (let n = 1; n <= 500; n += 1) {
        const expectedCode = buildLines(n);
        root.innerHTML = renderMarkdownHtml(buildFencedMarkdown(n));
        fold.sync(root);

        const frame = root.querySelector<HTMLElement>(".markdown-code-scroll");
        const code = root.querySelector<HTMLElement>("pre code");
        expect(frame).not.toBeNull();
        expect(code).not.toBeNull();
        expect(scrollportFrameParts(frame!)).not.toBeNull();

        const codeText = code!.textContent ?? "";
        expect(countFenceLines(codeText)).toBe(n);
        expect(codeText.trimEnd()).toBe(expectedCode);

        const toggle = frame!.querySelector<HTMLButtonElement>(".den-code-fold-toggle");
        const fade = frame!.querySelector<HTMLElement>(".den-code-fold-fade");
        const textNode = code!.firstChild;
        expect(textNode).not.toBeNull();

        if (n <= 24) {
          expect(frame!.dataset.codeFold).toBeUndefined();
          expect(toggle).toBeNull();
          expect(fade).toBeNull();
          expect(collapsedRevealHostsContaining(textNode!)).toHaveLength(0);
        } else {
          expect(frame!.dataset.codeFold).toBe("folded");
          expect(fade?.getAttribute("aria-hidden")).toBe("true");
          expect(toggle).not.toBeNull();
          expect(toggle!.textContent).toBe(`Show all ${n} lines`);
          expect(toggle!.getAttribute("aria-expanded")).toBe("false");
          expect(toggle!.getAttribute("aria-label")).toBe(`Show all ${n} lines`);
          assertNamedControl(toggle!, "button");
          expect(collapsedRevealHostsContaining(textNode!)).toHaveLength(1);
        }
      }

      // Check 24 lines with an explicit single trailing newline inside raw pre/code.
      root.innerHTML = `<div class="den-scrollport markdown-code-scroll" data-den-scrollport="x"><div class="den-scrollport__viewport"><pre class="den-scrollport__content"><code>${buildLines(24)}\n</code></pre></div></div>`;
      fold.sync(root);
      const exact24Frame = root.querySelector<HTMLElement>(".markdown-code-scroll")!;
      expect(exact24Frame.dataset.codeFold).toBeUndefined();
      expect(exact24Frame.querySelector(".den-code-fold-toggle")).toBeNull();
    } finally {
      fold.dispose();
      root.remove();
    }
  });

  it("folds synchronously before parent measurement effects and copies all 400 lines in both surfaces", () => {
    const copySpy = vi.spyOn(clipboard, "copyTextToClipboard").mockResolvedValue(undefined);
    const full400 = buildLines(400, "stmt");
    const markdown = `\`\`\`ts\n${full400}\n\`\`\``;

    let measuredFoldState: string | undefined;
    const proseView = render(() => {
      let row: HTMLDivElement | undefined;
      createEffect(() => {
        measuredFoldState = row?.querySelector<HTMLElement>(".markdown-code-scroll")?.dataset.codeFold;
      });
      return (
        <div ref={row}>
          <AssistantProseBody messageId="m-sync" source={markdown} />
        </div>
      );
    });
    expect(measuredFoldState).toBe("folded");

    const proseFrame = proseView.container.querySelector<HTMLElement>(".markdown-code-scroll")!;
    const proseToggle = proseFrame.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!;
    expect(proseToggle.textContent).toBe("Show all 400 lines");
    assertNamedControl(proseToggle, "button");
    fireEvent.click(proseFrame.querySelector<HTMLButtonElement>("[data-testid=code-block-copy]")!);
    expect(copySpy).toHaveBeenCalledTimes(1);
    expect((copySpy.mock.calls[0]?.[0] ?? "").trimEnd()).toBe(full400);
    proseView.unmount();

    const mdView = render(() => <MarkdownBody source={markdown} />);
    const mdFrame = mdView.container.querySelector<HTMLElement>(".markdown-code-scroll")!;
    expect(mdFrame.dataset.codeFold).toBe("folded");
    fireEvent.click(mdFrame.querySelector<HTMLButtonElement>("[data-testid=code-block-copy]")!);
    expect(copySpy).toHaveBeenCalledTimes(2);
    expect(countFenceLines(copySpy.mock.calls[1]?.[0] ?? "")).toBe(400);
    mdView.unmount();
  });

  it("integrates with FindController leases in both standalone and shared-store modes", async () => {
    const codeLines: string[] = [];
    for (let i = 1; i <= 60; i += 1) {
      if (i === 30) codeLines.push("const first = 'needle-token';");
      else if (i === 50) codeLines.push("const second = 'needle-token';");
      else codeLines.push(`const row${i} = ${i};`);
    }
    const markdown = [
      "Intro paragraph before fence.",
      "",
      "```ts",
      codeLines.join("\n"),
      "```",
      "",
      "Outro paragraph with needle-token after fence.",
    ].join("\n");

    for (const shared of [false, true]) {
      resetFindControllerForTests();
      const store = shared ? createTranscriptDisclosureStore() : undefined;
      const { container, unmount } = render(() =>
        store ? (
          <TranscriptDisclosureProvider value={store}>
            <AssistantProseBody messageId="msg-find" source={markdown} />
          </TranscriptDisclosureProvider>
        ) : (
          <MarkdownBody source={markdown} />
        ),
      );

      const root = container.querySelector<HTMLElement>(".markdown-body")!;
      const frame = container.querySelector<HTMLElement>(".markdown-code-scroll")!;
      const toggle = frame.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!;

      registerFindableView({
        id: "session-transcript",
        rootEl: () => root,
        scrollMatchIntoView: () => {},
      });
      setPrimaryFindableView("session-transcript");

      // Control copy inside the toggle and copy buttons is excluded from Find matches.
      openFind();
      setFindQuery("Show all 60");
      expect(findController.matches()).toHaveLength(0);
      setFindQuery("Copy");
      expect(findController.matches()).toHaveLength(0);

      // Typing counts folded matches without unfolding the fence.
      setFindQuery("needle-token");
      expect(findController.matches()).toHaveLength(3);
      expect(findController.collapsedCount()).toBe(2);
      expect(frame.dataset.codeFold).toBe("folded");
      expect(toggle.getAttribute("aria-expanded")).toBe("false");

      // First Next reveals match 0 inside the folded fence via lease.
      findNext();
      await flushMicrotasks();
      expect(frame.dataset.codeFold).toBe("open");
      expect(findController.activeIndex()).toBe(0);
      expect(findController.collapsedCount()).toBe(0);

      // Second Next advances to match 1 inside the same fence and retains the lease.
      findNext();
      await flushMicrotasks();
      expect(findController.activeIndex()).toBe(1);
      expect(frame.dataset.codeFold).toBe("open");

      // Third Next moves to the outro match outside the fence, releasing the lease.
      findNext();
      await flushMicrotasks();
      expect(findController.activeIndex()).toBe(2);
      expect(frame.dataset.codeFold).toBe("folded");
      expect(toggle.textContent).toBe("Show all 60 lines");

      // Moving back into the fence re-acquires the lease, and Escape releases it.
      findPrev();
      await flushMicrotasks();
      expect(frame.dataset.codeFold).toBe("open");
      expect(tryConsumeEscapeLadder()).toBe(true);
      expect(findController.isOpen()).toBe(false);
      expect(frame.dataset.codeFold).toBe("folded");

      // Close also restores clipping when active on a folded match.
      openFind();
      setFindQuery("needle-token");
      findNext();
      expect(frame.dataset.codeFold).toBe("open");
      closeFind();
      expect(frame.dataset.codeFold).toBe("folded");

      // When the user explicitly opens the fence, Find leases leave it open after Close.
      fireEvent.click(toggle);
      await flushMicrotasks();
      expect(frame.dataset.codeFold).toBe("open");
      expect(toggle.textContent).toBe("Show less");
      expect(toggle.getAttribute("aria-label")).toBe("Show less (60 lines)");
      expect(toggle.getAttribute("aria-expanded")).toBe("true");

      openFind();
      setFindQuery("needle-token");
      expect(findController.collapsedCount()).toBe(0);
      findNext();
      closeFind();
      expect(frame.dataset.codeFold).toBe("open");
      expect(toggle.textContent).toBe("Show less");

      unmount();
    }
  });

  it("persists fold state across virtualized row unmount and remount by message id and fence index", async () => {
    const store = createTranscriptDisclosureStore();
    const markdown = `${buildFencedMarkdown(32, "ts", "first")}\n\n${buildFencedMarkdown(48, "ts", "second")}`;
    const wire = {
      id: "msg-virtual-1",
      role: "assistant" as const,
      content: markdown,
      origin: "model" as const,
      authority: "none" as const,
      trust_tier: "untrusted" as const,
      created_at: "t",
    };

    const firstMount = render(() => (
      <TranscriptDisclosureProvider value={store}>
        <AssistantChatTurn
          item={{ kind: "assistant", key: "msg-virtual-1", text: markdown }}
          wireRow={() => wire}
        />
      </TranscriptDisclosureProvider>
    ));

    const firstFrames = firstMount.container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
    expect(firstFrames).toHaveLength(2);
    expect(firstFrames[0]!.dataset.codeFold).toBe("folded");
    expect(firstFrames[0]!.dataset.disclosureKey).toBe("code-fence:msg-virtual-1:0");
    expect(firstFrames[1]!.dataset.codeFold).toBe("folded");
    expect(firstFrames[1]!.dataset.disclosureKey).toBe("code-fence:msg-virtual-1:1");

    // Expand fence 0; leave fence 1 folded.
    fireEvent.click(
      firstFrames[0]!.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!,
    );
    await flushMicrotasks();
    expect(firstFrames[0]!.dataset.codeFold).toBe("open");
    expect(store.isUserOpen(transcriptDisclosureKey.codeFence("msg-virtual-1:0"))).toBe(true);
    expect(store.isUserOpen(transcriptDisclosureKey.codeFence("msg-virtual-1:1"))).toBe(false);

    // Unmount completely as when scrolled out of the virtualized viewport.
    firstMount.unmount();

    // Remount a new row instance for the same message; fence 0 stays open, fence 1 stays folded.
    const remounted = render(() => (
      <TranscriptDisclosureProvider value={store}>
        <AssistantChatTurn
          item={{ kind: "assistant", key: "msg-virtual-1", text: markdown }}
          wireRow={() => wire}
        />
      </TranscriptDisclosureProvider>
    ));
    const remountedFrames = remounted.container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
    expect(remountedFrames[0]!.dataset.codeFold).toBe("open");
    expect(
      remountedFrames[0]!.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")?.textContent,
    ).toBe("Show less");
    expect(remountedFrames[1]!.dataset.codeFold).toBe("folded");

    // Restoring keys on the store updates mounted fences reactively.
    store.restoreUserOpenKeys([transcriptDisclosureKey.codeFence("msg-virtual-1:1")]);
    expect(remountedFrames[0]!.dataset.codeFold).toBe("folded");
    expect(remountedFrames[1]!.dataset.codeFold).toBe("open");
    remounted.unmount();
  });

  it("preserves user fold state by fence index across re-renders and cleans up hosts", async () => {
    const [source, setSource] = createSignal(
      `${buildFencedMarkdown(30, "ts", "first")}\n\n${buildFencedMarkdown(40, "ts", "second")}`,
    );

    const { container, unmount } = render(() => <MarkdownBody source={source()} />);
    await flushMicrotasks();

    const initialFrames = container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
    expect(initialFrames).toHaveLength(2);
    expect(initialFrames[0]!.dataset.codeFold).toBe("folded");
    expect(initialFrames[1]!.dataset.codeFold).toBe("folded");

    // User opens fence 0; fence 1 stays folded.
    fireEvent.click(
      initialFrames[0]!.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!,
    );
    await flushMicrotasks();
    expect(initialFrames[0]!.dataset.codeFold).toBe("open");
    expect(initialFrames[1]!.dataset.codeFold).toBe("folded");

    // Streaming re-renders innerHTML with fence 0 grown to 35 lines.
    setSource(
      `${buildFencedMarkdown(35, "ts", "first")}\n\n${buildFencedMarkdown(40, "ts", "second")}`,
    );
    await waitFor(() => {
      const updatedFrames = container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
      expect(updatedFrames).toHaveLength(2);
      expect(updatedFrames[0]!.dataset.codeFold).toBe("open");
      expect(
        updatedFrames[0]!
          .querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")
          ?.getAttribute("aria-label"),
      ).toBe("Show less (35 lines)");
      expect(updatedFrames[1]!.dataset.codeFold).toBe("folded");
    });

    // Shrinking fence 1 to <= 24 lines strips fold chrome and unregisters its reveal host.
    setSource(
      `${buildFencedMarkdown(35, "ts", "first")}\n\n${buildFencedMarkdown(20, "ts", "second")}`,
    );
    await waitFor(() => {
      const shrunkFrames = container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
      expect(shrunkFrames).toHaveLength(2);
      expect(shrunkFrames[0]!.dataset.codeFold).toBe("open");
      expect(shrunkFrames[1]!.dataset.codeFold).toBeUndefined();
      expect(shrunkFrames[1]!.querySelector(".den-code-fold-toggle")).toBeNull();
      const secondTextNode = shrunkFrames[1]!.querySelector("code")!.firstChild!;
      expect(collapsedRevealHostsContaining(secondTextNode)).toHaveLength(0);
    });

    // Fold fence 0 again, capture its code text node, unmount, and verify host cleanup.
    const activeFrame0 = container.querySelectorAll<HTMLElement>(".markdown-code-scroll")[0]!;
    fireEvent.click(
      activeFrame0.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!,
    );
    await flushMicrotasks();
    const firstTextNode = activeFrame0.querySelector("code")!.firstChild!;
    expect(collapsedRevealHostsContaining(firstTextNode)).toHaveLength(1);

    unmount();
    expect(collapsedRevealHostsContaining(firstTextNode)).toHaveLength(0);
  });

  it("animates expand and collapse through the shared height-toggle motion contract", async () => {
    const viewport = createTranscriptViewportController({ sessionId: () => "fold-motion" });
    const view = render(() => (
      <TranscriptViewportProvider value={viewport}>
        <MarkdownBody disclosureScope="msg-anim" source={buildFencedMarkdown(60, "ts", "anim")} />
      </TranscriptViewportProvider>
    ));

    const frame = view.container.querySelector<HTMLElement>(".markdown-code-scroll")!;
    const pre = frame.querySelector<HTMLElement>("pre")!;
    const code = frame.querySelector<HTMLElement>("code")!;
    const toggle = frame.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!;

    pre.style.paddingTop = "10px";
    code.style.lineHeight = "18px";
    const expectedFolded = 12 * 18 + 10 + 30; // 256px
    Object.defineProperty(frame, "scrollHeight", { configurable: true, value: 1120 });
    frame.getBoundingClientRect = () =>
      new DOMRect(0, 0, 600, frame.dataset.codeFold === "open" ? 1120 : expectedFolded);
    toggle.getBoundingClientRect = () => new DOMRect(0, 0, 600, 30);

    const animations: Array<{ keyframes: unknown; finish: () => void }> = [];
    frame.animate = vi.fn((keyframes) => {
      const anim = {
        cancel: vi.fn(),
        onfinish: null as ((ev: AnimationPlaybackEvent) => void) | null,
        oncancel: null as ((ev: AnimationPlaybackEvent) => void) | null,
      };
      animations.push({
        keyframes,
        finish: () => anim.onfinish?.call(anim as unknown as Animation, new Event("finish") as AnimationPlaybackEvent),
      });
      return anim as unknown as Animation;
    });

    // Opening animates from folded height (256px) to scrollHeight (1120px).
    fireEvent.click(toggle, { detail: 1 });
    expect(animations).toHaveLength(1);
    expect(animations[0]!.keyframes).toEqual({ height: [`${expectedFolded}px`, "1120px"] });
    expect(frame.dataset.animating).toBe("true");
    expect(frame.dataset.codeFold).toBe("open");
    animations[0]!.finish();
    await nextFrame();
    expect(frame.dataset.animating).toBeUndefined();

    // Closing computes the 12-code-line folded height without layout mutation and animates down.
    fireEvent.click(toggle, { detail: 1 });
    expect(animations).toHaveLength(2);
    expect(animations[1]!.keyframes).toEqual({ height: ["1120px", `${expectedFolded}px`] });
    expect(frame.dataset.closing).toBe("true");
    animations[1]!.finish();
    await nextFrame();
    expect(frame.dataset.codeFold).toBe("folded");
    expect(frame.dataset.closing).toBeUndefined();

    view.unmount();
  });

  it("preserves the transcript reading anchor when expanding and collapsing a 400-line fence", async () => {
    const viewport = createTranscriptViewportController({
      sessionId: () => "fold-anchor-session",
    });

    let streamEl!: HTMLDivElement;
    let rowEl!: HTMLDivElement;

    const { unmount } = render(() => (
      <TranscriptViewportProvider value={viewport}>
        <div class="den-chat-stream" ref={streamEl}>
          <div style={{ height: "5000px" }} />
          <div
            class="transcript-viewport-row"
            data-index="5"
            data-virtual-start="5000"
            ref={rowEl}
          >
            <MarkdownBody
              disclosureScope="msg-anchor"
              source={buildFencedMarkdown(400, "ts", "tall")}
            />
          </div>
          <div class="transcript-viewport-row" data-index="6" data-virtual-start="12220">
            <p>Anchored next message</p>
          </div>
        </div>
      </TranscriptViewportProvider>
    ));

    bindScrollportMotion(streamEl, streamEl, streamEl);
    viewport.attachStream(streamEl);

    const frame = rowEl.querySelector<HTMLElement>(".markdown-code-scroll")!;
    const toggle = frame.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!;
    let layoutEvents = 0;
    rowEl.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, () => {
      layoutEvents += 1;
    });

    // Model layout geometry: row starts at document y=5000; folded=240px, open=7200px.
    const rowStart = 5000;
    const frameHeight = () => (frame.dataset.codeFold === "open" ? 7200 : 240);
    Object.defineProperties(streamEl, {
      clientHeight: { configurable: true, get: () => 600 },
      scrollHeight: { configurable: true, get: () => rowStart + frameHeight() + 2000 },
    });
    mockScrollerMotion(streamEl);
    vi.spyOn(streamEl, "getBoundingClientRect").mockImplementation(() =>
      new DOMRect(0, 0, 700, 600),
    );
    vi.spyOn(frame, "getBoundingClientRect").mockImplementation(() =>
      new DOMRect(0, rowStart - streamEl.scrollTop, 700, frameHeight()),
    );
    vi.spyOn(toggle, "getBoundingClientRect").mockImplementation(() =>
      new DOMRect(0, rowStart + frameHeight() - 30 - streamEl.scrollTop, 700, 30),
    );

    try {
      // 1. Expanding while tail-following stops following instead of jumping to line 400.
      streamEl.scrollTop = 4940;
      expect(viewport.following()).toBe(true);
      fireEvent.click(toggle, { detail: 1 });
      flushScrollportFrameForTests(streamEl);
      await flushMicrotasks();
      expect(frame.dataset.codeFold).toBe("open");
      expect(viewport.following()).toBe(false);
      expect(streamEl.scrollTop).toBe(4940);
      expect(layoutEvents).toBeGreaterThanOrEqual(1);

      // 2. Reader scrolls to the bottom of the 400-line fence and clicks "Show less".
      viewport.stopFollowing();
      streamEl.scrollTop = 11900;
      const buttonTopBefore = toggle.getBoundingClientRect().top;
      expect(buttonTopBefore).toBe(270);

      const layoutBeforeCollapse = layoutEvents;
      fireEvent.click(toggle, { detail: 1 });
      flushScrollportFrameForTests(streamEl);
      await flushMicrotasks();
      expect(frame.dataset.codeFold).toBe("folded");
      expect(layoutEvents).toBeGreaterThan(layoutBeforeCollapse);

      // ScrollportPressAnchor shifts by -6960 so the pressed button and next message stay in place.
      expect(streamEl.scrollTop).toBe(4940);
      expect(toggle.getBoundingClientRect().top).toBe(buttonTopBefore);

      // 3. When the fence top is already visible on screen, collapsing keeps scrollTop steady.
      fireEvent.click(toggle, { detail: 1 });
      flushScrollportFrameForTests(streamEl);
      await flushMicrotasks();
      expect(frame.dataset.codeFold).toBe("open");
      streamEl.scrollTop = 4940;
      expect(frame.getBoundingClientRect().top).toBe(60);

      fireEvent.click(toggle, { detail: 1 });
      flushScrollportFrameForTests(streamEl);
      await flushMicrotasks();
      expect(frame.dataset.codeFold).toBe("folded");
      expect(streamEl.scrollTop).toBe(4940);
      expect(frame.getBoundingClientRect().top).toBe(60);
    } finally {
      viewport.attachStream(null);
      unbindScrollportMotion(streamEl);
      unmount();
    }
  });
});
