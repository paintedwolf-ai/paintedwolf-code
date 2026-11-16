import { afterEach, describe, expect, it, vi } from "vitest";
import type { WindowEdgeChrome } from "../../platform/windows/window-chrome-layout.ts";
import {
  nativeWindowEdgeAttributes,
  windowEdgeAttributes,
  windowEdgeClaims,
  windowEdgeHosts,
  type WindowEdgeLayout,
} from "./window-edges.ts";

const chrome = vi.hoisted(() => ({
  left: null as WindowEdgeChrome | null,
  right: null as WindowEdgeChrome | null,
}));
vi.mock("../../platform/windows/window-chrome-layout.ts", () => ({
  windowEdgeChrome: (side: "left" | "right") => chrome[side],
}));

afterEach(() => {
  chrome.left = null;
  chrome.right = null;
});

const split: WindowEdgeLayout = {
  navSide: null,
  split: true,
  stageOccupied: true,
  contextHidden: false,
  conversationHidden: false,
  stageSide: "left",
  narrowSurvivor: "chat",
  drawerSide: null,
};
const hosts = (layout: WindowEdgeLayout) => [windowEdgeHosts("left", layout), windowEdgeHosts("right", layout)];

describe("window edge hosts", () => {
  it("gives a showing sidebar its own edge on either side", () => {
    expect(hosts({ ...split, navSide: "left" })).toEqual([
      { owner: "nav", narrow: "nav" }, { owner: "chat", narrow: "chat" },
    ]);
    expect(hosts({ ...split, navSide: "right", stageSide: "right" })).toEqual([
      { owner: "chat", narrow: "chat" }, { owner: "nav", narrow: "nav" },
    ]);
  });

  it("splits the edges between the columns and hands a dropped column's edge to the narrow survivor", () => {
    expect(hosts(split)).toEqual([{ owner: "stage", narrow: "chat" }, { owner: "chat", narrow: "chat" }]);
    expect(hosts({ ...split, stageSide: "right" })).toEqual([
      { owner: "chat", narrow: "chat" }, { owner: "stage", narrow: "chat" },
    ]);
    expect(hosts({ ...split, narrowSurvivor: "stage" })).toEqual([
      { owner: "stage", narrow: "stage" }, { owner: "chat", narrow: "stage" },
    ]);
  });

  it("gives the remaining column both edges when the other is hidden or absent", () => {
    const stage = { owner: "stage", narrow: "stage" };
    const chat = { owner: "chat", narrow: "chat" };
    expect(hosts({ ...split, conversationHidden: true })).toEqual([stage, stage]);
    expect(hosts({ ...split, contextHidden: true })).toEqual([chat, chat]);
    expect(hosts({ ...split, split: false })).toEqual([stage, stage]);
    expect(hosts({ ...split, split: false, stageOccupied: false })).toEqual([chat, chat]);
  });

  it("hands an open drawer the edge its column held on the drawer's side", () => {
    expect(hosts({ ...split, drawerSide: "right" })).toEqual([
      { owner: "stage", narrow: "chat" }, { owner: "drawer", narrow: "drawer" },
    ]);
    expect(hosts({ ...split, conversationHidden: true, drawerSide: "right" })).toEqual([
      { owner: "stage", narrow: "stage" }, { owner: "drawer", narrow: "drawer" },
    ]);
    expect(hosts({ ...split, split: false, drawerSide: "left" })).toEqual([
      { owner: "drawer", narrow: "drawer" }, { owner: "stage", narrow: "stage" },
    ]);
  });

  it("leaves the edge with the other column while the drawer's column is away from it", () => {
    // The conversation is on the left, so its drawer opens mid-window above the narrow band.
    expect(hosts({ ...split, stageSide: "right", drawerSide: "right" })).toEqual([
      { owner: "chat", narrow: "chat" }, { owner: "stage", narrow: "drawer" },
    ]);
  });

  it("never takes the sidebar's edge", () => {
    expect(windowEdgeHosts("right", { ...split, navSide: "right", stageSide: "right", drawerSide: "right" }))
      .toEqual({ owner: "nav", narrow: "nav" });
  });
});

describe("window edge claims", () => {
  it("claims only edges that carry controls, with how they reach the screen", () => {
    chrome.right = "drawn";
    expect(windowEdgeClaims("chat", split)).toEqual([{ side: "right", chrome: "drawn", owner: true, narrow: true }]);
    expect(windowEdgeClaims("stage", split)).toEqual([]);
    chrome.left = "native";
    chrome.right = null;
    expect(windowEdgeClaims("stage", split)).toEqual([{ side: "left", chrome: "native", owner: true, narrow: false }]);
    expect(windowEdgeClaims("chat", split)).toEqual([{ side: "left", chrome: "native", owner: false, narrow: true }]);
  });

  it("moves a column's claim to the drawer while it is open", () => {
    chrome.right = "drawn";
    const open = { ...split, drawerSide: "right" as const };
    expect(windowEdgeClaims("drawer", open)).toEqual([{ side: "right", chrome: "drawn", owner: true, narrow: true }]);
    expect(windowEdgeClaims("chat", open)).toEqual([]);
    expect(windowEdgeClaims("drawer", split)).toEqual([]);
  });
});

describe("window edge attributes", () => {
  it("marks the edge, its chrome, and the side of the narrow band a partial claim holds on", () => {
    expect(windowEdgeAttributes({ side: "right", chrome: "drawn", owner: true, narrow: true })).toEqual({
      "data-window-edge": "right", "data-window-edge-chrome": "drawn",
      "data-narrow-only": undefined, "data-narrow-hidden": undefined,
    });
    expect(windowEdgeAttributes({ side: "left", chrome: "native", owner: false, narrow: true }))
      .toMatchObject({ "data-narrow-only": "", "data-narrow-hidden": undefined });
    expect(windowEdgeAttributes({ side: "left", chrome: "native", owner: true, narrow: false }))
      .toMatchObject({ "data-narrow-only": undefined, "data-narrow-hidden": "" });
    expect(windowEdgeAttributes(undefined)).toEqual({});
  });

  it("marks a header only for buttons the OS overlays on it", () => {
    expect(nativeWindowEdgeAttributes([{ side: "right", chrome: "drawn", owner: true, narrow: true }])).toEqual({});
    expect(nativeWindowEdgeAttributes([{ side: "left", chrome: "native", owner: true, narrow: true }]))
      .toMatchObject({ "data-window-edge": "left", "data-window-edge-chrome": "native" });
  });
});
