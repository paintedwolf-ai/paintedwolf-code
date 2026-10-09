// @vitest-environment jsdom
import { transcriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";

import { flushScrollportFrameForTests, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { describe, expect, it, vi } from "vitest";

import { cancelStreamSpringScroll } from "./stream-scroll-spring.ts";

import { installViewportFixtureCleanup, followingFixture, runtime, frame } from "./transcript-viewport-test-fixture.ts";
installViewportFixtureCleanup();
describe("following the latest message", () => {
  it("End resumes following and reaches an expanded tail before paint", () => {
    const f = followingFixture("s-expand-end", { content: 300, scrollTop: 0 });
    const stop = f.controller.bindRuntime(runtime("s-expand-end"));
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    f.resize(900);
    finish();
    const event = new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true });
    f.stream.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(600);
    stop();
  });

  it("holds a short transcript through expansion, later geometry, and arrivals", () => {
    const f = followingFixture("s-expand", { content: 300, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    for (const height of [300, 400, 900, 1_400]) {
      f.resize(height);
      expect(f.stream.scrollTop).toBe(0);
    }
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    f.controller.contentChanged({ delivery: "structural" });
    f.resize(1_600);
    f.controller.contentChanged({ delivery: "prose", rowKey: "answer", firstContent: true });
    f.resize(1_800);
    expect(f.stream.scrollTop).toBe(0);
    f.controller.jumpToTail(false);
    f.resize(2_000);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("keeps following when expanded content still fits", () => {
    const f = followingFixture("s-expand-fits", { content: 200, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    f.resize(280);
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(true);
    f.resize(500);
    expect(f.stream.scrollTop).toBe(200);
  });

  it("waits for the final row measurement before restoring following", () => {
    const f = followingFixture("s-expand-measure", { content: 300, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    scheduleScrollportFrame(f.stream, "measure", () => f.resize(900));
    finish();
    expect(f.controller.following()).toBe(false);
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(0);
  });

  it("waits for child expansion and does not treat a nearly visible tail as visible", () => {
    const f = followingFixture("s-expand-child", { content: 300, scrollTop: 0 });
    const finishParent = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    const finishChild = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("tool"), "open");
    finishParent();
    expect(f.controller.following()).toBe(false);
    f.resize(340);
    finishChild();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(0);
  });

  it("preserves an existing reading position through expansion", () => {
    const f = followingFixture("s-expand-reading", { content: 1_000, scrollTop: 250 });
    f.controller.stopFollowing();
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("tool"), "open");
    f.resize(1_300);
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.stream.scrollTop).toBe(250);
    expect(f.controller.following()).toBe(false);
  });

  it("selection during an expansion prevents following from being restored", () => {
    const f = followingFixture("s-expand-selection", { content: 300, scrollTop: 0 });
    const stop = f.controller.bindRuntime(runtime("s-expand-selection"));
    const text = document.createTextNode("Expanded content to select");
    f.stream.append(text);
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    const range = document.createRange();
    range.selectNodeContents(text);
    document.getSelection()?.removeAllRanges();
    document.getSelection()?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    document.getSelection()?.removeAllRanges();
    stop();
  });

  it.each(["reading", "jump", "session", "detach"])("a settled expansion cannot override later %s intent", (action) => {
    const f = followingFixture(`s-expand-${action}`, { content: 300, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    finish();
    if (action === "reading") f.controller.stopFollowing();
    if (action === "jump") f.controller.jumpToTail(false);
    if (action === "session") {
      f.controller.activateSession("other-session");
      f.controller.stopFollowing();
    }
    if (action === "detach") f.controller.attachStream(null);
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(action === "jump");
  });

  it("resumes following and repins to tail when closing a disclosure opened at the tail", () => {
    const f = followingFixture("s-motion-tail", { content: 2_000, scrollTop: 1_700 });
    expect(f.controller.following()).toBe(true);

    const finishOpen = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "open");
    f.resize(3_200);
    finishOpen();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_700);

    const finishClose = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "close");
    f.resize(2_000);
    finishClose();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  // An accordion closes the open row and opens the pressed one in one motion, in either call order.
  it.each([
    ["close then open", ["close", "open"]],
    ["open then close", ["open", "close"]],
  ] as const)("treats a joined motion with an expansion as an expansion (%s)", (_, order) => {
    const f = followingFixture("s-motion-accordion", { content: 2_000, scrollTop: 1_700 });
    expect(f.controller.following()).toBe(true);

    const finishes = order.map((direction) =>
      f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool(direction === "open" ? "row-b" : "row-a"), direction));
    f.resize(2_300);
    for (const finish of finishes) finish();
    flushScrollportFrameForTests(f.stream);

    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("does not resume following when closing a disclosure in historical messages", () => {
    const f = followingFixture("s-motion-history", { content: 4_000, scrollTop: 1_000 });
    f.controller.stopFollowing();

    const finishOpen = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("hist-card"), "open");
    f.resize(4_500);
    finishOpen();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);

    const finishClose = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("hist-card"), "close");
    f.resize(4_000);
    finishClose();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_000);
  });

  it("does not resume following on close if the user scrolled away during motion", () => {
    const f = followingFixture("s-motion-scrolled", { content: 2_000, scrollTop: 1_700 });
    const stop = f.controller.bindRuntime(runtime("s-motion-scrolled"));

    const finishOpen = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "open");
    f.resize(3_200);
    f.stream.dispatchEvent(
      new WheelEvent("wheel", { deltaY: -100, bubbles: true, cancelable: true }),
    );
    f.scrollTo(500);
    finishOpen();
    flushScrollportFrameForTests(f.stream);

    const finishClose = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "close");
    f.resize(2_000);
    finishClose();
    flushScrollportFrameForTests(f.stream);

    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(500);
    stop();
  });

  it("keeps the latest message in view as content grows, before paint", () => {
    const f = followingFixture("s-grow");
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(2_100);
    f.resize(1_900);
    expect(f.stream.scrollTop).toBe(1_600);
  });

  it("keeps the latest line in view above a growing composer", () => {
    const f = followingFixture("s-composer");
    f.resize(2_000, 240);
    expect(f.stream.scrollTop).toBe(1_760);
  });

  it("keeps following when a new card arrives", () => {
    const f = followingFixture("s-card");
    f.controller.contentChanged({ delivery: "structural" });
    expect(f.controller.following()).toBe(true);
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(2_100);
  });

  it("keeps the reader's place when a card arrives while they read", () => {
    const f = followingFixture("s-card-reading");
    f.controller.stopFollowing();
    f.controller.contentChanged({ delivery: "structural" });
    expect(f.controller.following()).toBe(false);
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("keeps the reader's place after they act on the transcript", () => {
    const f = followingFixture("s-act");
    f.controller.stopFollowing();
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("resumes following on a user send and lands on the latest message", () => {
    const f = followingFixture("s-send");
    f.controller.stopFollowing();
    f.scrollTo(500);
    f.setContent(2_300);

    f.controller.jumpToTail(false);

    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(2_000);
  });

  it("a confirmed send arriving after reader input never resumes following", () => {
    const f = followingFixture("s-confirm");
    f.controller.jumpToTail(false);
    f.controller.stopFollowing();
    f.scrollTo(500);
    f.setContent(2_300);
    f.controller.contentChanged({ delivery: "structural" });
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(500);
    f.resize(2_500);
    expect(f.stream.scrollTop).toBe(500);
  });

  it("repins through tab chrome changes", () => {
    const f = followingFixture("s-chrome");
    f.setContent(2_200);
    f.controller.chromeChanged({ tabOpen: true, panelRetracted: false, panelHeightPx: 0 });
    expect(f.stream.scrollTop).toBe(1_900);
  });

  it("glides to the latest message on jump and then keeps it in view", async () => {
    const f = followingFixture("s-jump");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);
    expect(f.controller.following()).toBe(true);
    await vi.waitFor(() => expect(f.stream.scrollTop).toBe(1_700));
    await frame();

    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(2_100);
  });

  it("keeps the reader where an interrupted jump stopped", async () => {
    const f = followingFixture("s-jump-interrupted");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);
    cancelStreamSpringScroll(f.stream);
    await Promise.resolve();
    await Promise.resolve();

    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(200);
  });

  it("lands a jump on the latest message even when a card arrives mid-glide", async () => {
    const f = followingFixture("s-jump-card");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);
    f.controller.contentChanged({ delivery: "structural" });

    expect(f.controller.following()).toBe(true);
    await vi.waitFor(() => expect(f.stream.scrollTop).toBe(1_700));
  });

  it("lands a send from the tail at once and keeps pinning what follows", () => {
    const f = followingFixture("s-jump-on-tail");
    expect(f.stream.scrollTop).toBe(1_700);

    // The optimistic row is already in the DOM, farther below than a glance.
    f.setContent(2_150);
    f.controller.jumpToTail(true);
    expect(f.stream.scrollTop).toBe(1_850);

    // Its measurement, then the composer's activity lane, reach the tail before paint.
    f.resize(2_180);
    expect(f.stream.scrollTop).toBe(1_880);
    f.resize(2_180, 278);
    expect(f.stream.scrollTop).toBe(1_902);
  });

  it("jumps at once under reduced motion", () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true }) as MediaQueryList));
    const f = followingFixture("s-jump-reduced");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);

    expect(f.stream.scrollTop).toBe(1_700);
  });
});
