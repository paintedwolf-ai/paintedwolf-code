// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

const runtime = vi.hoisted(() => ({
  speaks: false,
  announce: vi.fn(async (_text: string, _politeness: string) => undefined),
}));
vi.mock("./host-announcement.ts", () => ({
  hostSpeaksAnnouncements: () => runtime.speaks,
  announceThroughHost: (text: string, politeness: string) => runtime.announce(text, politeness),
}));

import { classifyLiveRegion, installLiveRegionBridge, setupLiveRegionBridge } from "./live-region-bridge.ts";

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
const disposers: (() => void)[] = [];

function mount(html: string) {
  const root = document.createElement("div");
  root.innerHTML = html;
  document.body.append(root);
  const announce = vi.fn();
  disposers.push(installLiveRegionBridge(root, announce), () => root.remove());
  return { root, announce, get: (selector: string) => root.querySelector(selector) as HTMLElement };
}

afterEach(() => {
  for (const dispose of disposers.splice(0)) dispose();
  runtime.speaks = false;
  runtime.announce.mockClear();
  document.documentElement.removeAttribute("aria-live");
});

describe("classifyLiveRegion", () => {
  it("reads politeness and atomicity from the author's aria-live and the role", () => {
    const element = (html: string) => {
      const host = document.createElement("div");
      host.innerHTML = html;
      return host.firstElementChild as Element;
    };
    const status = element('<p role="status"></p>');
    expect(classifyLiveRegion(status, null)).toEqual({ politeness: "polite", atomic: true });
    expect(classifyLiveRegion(element('<div role="alert"></div>'), null)).toEqual({ politeness: "assertive", atomic: true });
    expect(classifyLiveRegion(element('<div role="log"></div>'), null)).toEqual({ politeness: "polite", atomic: false });
    expect(classifyLiveRegion(element("<output></output>"), null)).toEqual({ politeness: "polite", atomic: true });
    expect(classifyLiveRegion(element('<div aria-atomic="true"></div>'), "assertive")).toEqual({ politeness: "assertive", atomic: true });
    expect(classifyLiveRegion(status, "off")).toEqual({ politeness: "off", atomic: true });
    expect(classifyLiveRegion(element('<div role="note"></div>'), null)).toBeUndefined();
    expect(classifyLiveRegion(element("<div></div>"), "rude")).toBeUndefined();
  });
});

describe("installLiveRegionBridge", () => {
  it("silences every live region and leaves silent ones alone", async () => {
    const { root, get } = mount(`
      <p role="status" id="status">Ready</p>
      <div aria-live="assertive" id="assertive"></div>
      <div aria-live="off" id="off"></div>`);
    expect(get("#status").getAttribute("aria-live")).toBe("off");
    expect(get("#assertive").getAttribute("aria-live")).toBe("off");
    expect(get("#off").getAttribute("aria-live")).toBe("off");

    const inserted = document.createElement("span");
    inserted.setAttribute("role", "alert");
    root.append(inserted);
    await settle();
    expect(inserted.getAttribute("aria-live")).toBe("off");
  });

  it("forwards an atomic region's whole text with its politeness", async () => {
    const { announce, get } = mount('<p role="status" id="status">Ready <span aria-hidden="true">●</span></p>');
    get("#status").firstChild!.textContent = "Saved ";
    await settle();
    expect(announce).toHaveBeenCalledTimes(1);
    expect(announce).toHaveBeenCalledWith("Saved", "polite");
  });

  it("forwards only what a non-atomic region gained", async () => {
    const { announce, get } = mount('<div role="log" id="log"><p>Earlier</p></div>');
    const line = document.createElement("p");
    line.textContent = "Assistant: done";
    get("#log").append(line);
    await settle();
    expect(announce).toHaveBeenCalledWith("Assistant: done", "polite");
  });

  it("announces an inserted region only when it is assertive", async () => {
    const { root, announce } = mount("");
    const polite = document.createElement("p");
    polite.setAttribute("role", "status");
    polite.textContent = "Loaded";
    const alert = document.createElement("p");
    alert.setAttribute("role", "alert");
    alert.textContent = "Could not save";
    root.append(polite, alert);
    await settle();
    expect(announce.mock.calls).toEqual([["Could not save", "assertive"]]);

    polite.textContent = "Loaded again";
    await settle();
    expect(announce).toHaveBeenLastCalledWith("Loaded again", "polite");
  });

  it("lets a nearer silent subtree or hidden content stay quiet", async () => {
    const { announce, get } = mount(`
      <section role="log" aria-live="polite" id="log">
        <div aria-live="off" id="visual"></div>
        <span id="announcement"></span>
        <span hidden id="hidden"></span>
      </section>
      <div hidden><p role="status" id="concealed">Old</p></div>`);
    const row = document.createElement("article");
    row.textContent = "Streaming row";
    get("#visual").append(row);
    get("#hidden").textContent = "Hidden";
    get("#concealed").textContent = "New";
    await settle();
    expect(announce).not.toHaveBeenCalled();

    get("#announcement").textContent = "Assistant: done";
    await settle();
    expect(announce.mock.calls).toEqual([["Assistant: done", "polite"]]);
  });

  it("follows the author's later politeness while keeping the region silent", async () => {
    const { announce, get } = mount('<div aria-live="polite" id="rail"><p>One</p></div>');
    const rail = get("#rail");
    rail.setAttribute("aria-live", "off");
    await settle();
    rail.append(Object.assign(document.createElement("p"), { textContent: "Muted" }));
    await settle();
    expect(announce).not.toHaveBeenCalled();

    rail.setAttribute("aria-live", "assertive");
    await settle();
    expect(rail.getAttribute("aria-live")).toBe("off");
    rail.append(Object.assign(document.createElement("p"), { textContent: "Heard" }));
    await settle();
    expect(announce.mock.calls).toEqual([["Heard", "assertive"]]);

    rail.setAttribute("role", "status");
    rail.removeAttribute("aria-atomic");
    await settle();
    rail.lastElementChild!.textContent = "Changed";
    await settle();
    expect(announce).toHaveBeenLastCalledWith("One Muted Changed", "assertive");
  });

  it("ignores changes outside regions, drops a region whose role goes away, and skips hidden or comment content", async () => {
    const { announce, get } = mount(`
      <p id="plain">Plain</p>
      <p role="status" id="former">Was a status</p>
      <div role="status" id="atomic">Saved<!-- note --><span hidden>secret</span></div>`);
    get("#plain").textContent = "Changed";
    get("#former").removeAttribute("role");
    await settle();
    get("#former").textContent = "No longer live";
    await settle();
    expect(announce).not.toHaveBeenCalled();

    get("#atomic").append(Object.assign(document.createElement("b"), { textContent: "now" }));
    await settle();
    expect(announce.mock.calls).toEqual([["Saved now", "polite"]]);
  });

  it("stops forwarding once disposed", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<p role="status">A</p>';
    document.body.append(root);
    const announce = vi.fn();
    const dispose = installLiveRegionBridge(root, announce);
    dispose();
    root.firstElementChild!.textContent = "B";
    await settle();
    expect(announce).not.toHaveBeenCalled();
    root.remove();
  });
});

describe("setupLiveRegionBridge", () => {
  it("installs only where the host speaks announcements", async () => {
    const region = document.createElement("p");
    region.setAttribute("role", "status");
    document.body.append(region);
    setupLiveRegionBridge();
    expect(region.hasAttribute("aria-live")).toBe(false);

    runtime.speaks = true;
    setupLiveRegionBridge();
    expect(region.getAttribute("aria-live")).toBe("off");
    region.textContent = "Saved";
    await settle();
    expect(runtime.announce).toHaveBeenCalledWith("Saved", "polite");
    region.remove();
  });
});
