/**
 * Forwards every live region's announcements to the host and keeps the regions
 * themselves silent, so the host is the one channel a screen reader hears.
 *
 * Regions are the ARIA ones: an explicit `aria-live`, or the implicit live roles
 * `status`, `log` and `alert` (and `<output>`). The author's politeness is kept
 * aside and the element's `aria-live` is rewritten to `off`; later author writes
 * update the kept politeness. A change belongs to its nearest region, and a
 * nearer `aria-live="off"` silences it, as in ARIA. Atomic regions announce
 * their whole text; others announce what was added. A newly inserted region
 * announces only when assertive.
 */
import {
  announceThroughHost,
  hostSpeaksAnnouncements,
  type AnnouncementPoliteness,
} from "./host-announcement.ts";

type Politeness = AnnouncementPoliteness | "off";

type Region = { politeness: Politeness; atomic: boolean };

export type Announce = (text: string, politeness: AnnouncementPoliteness) => void;

export const LIVE_REGION_SELECTOR =
  '[aria-live], [role~="status"], [role~="log"], [role~="alert"], output';

const IMPLICIT: Record<string, Region> = {
  status: { politeness: "polite", atomic: true },
  log: { politeness: "polite", atomic: false },
  alert: { politeness: "assertive", atomic: true },
  output: { politeness: "polite", atomic: true },
};

function implicitRegion(element: Element): Region | undefined {
  const roles = (element.getAttribute("role") ?? "").split(/\s+/);
  const role = roles.find((token) => token in IMPLICIT);
  if (role) return IMPLICIT[role];
  return element.localName === "output" ? IMPLICIT.output : undefined;
}

function politenessOf(value: string | null): Politeness | undefined {
  return value === "polite" || value === "assertive" || value === "off" ? value : undefined;
}

/** The region an element declares, from the author's `aria-live` and its role. */
export function classifyLiveRegion(element: Element, authorLive: string | null): Region | undefined {
  const implicit = implicitRegion(element);
  const explicit = politenessOf(authorLive);
  if (!implicit && !explicit) return undefined;
  const atomicAttribute = element.getAttribute("aria-atomic");
  return {
    politeness: explicit ?? implicit?.politeness ?? "off",
    atomic: atomicAttribute === null ? implicit?.atomic ?? false : atomicAttribute === "true",
  };
}

function readableText(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return (node as Text).data;
  if (!(node instanceof Element)) return "";
  if (node.hasAttribute("hidden") || node.getAttribute("aria-hidden") === "true") return "";
  // Element boundaries become word breaks, as speech reads separate rows apart.
  let text = "";
  for (const child of node.childNodes) text += child instanceof Element ? ` ${readableText(child)} ` : readableText(child);
  return text;
}

function normalized(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

function concealed(element: Element): boolean {
  return element.closest('[hidden], [aria-hidden="true"], [inert]') !== null;
}

function hiddenBelow(node: Node, region: Element): boolean {
  for (let element = node.parentElement; element && element !== region; element = element.parentElement) {
    if (element.hasAttribute("hidden") || element.getAttribute("aria-hidden") === "true") return true;
  }
  return false;
}

export function installLiveRegionBridge(root: Element, announce: Announce): () => void {
  const authorLive = new WeakMap<Element, string | null>();
  const regions = new WeakMap<Element, Region>();
  const ownWrites = new WeakMap<Element, number>();
  // Writes made before observing raise no record to discount.
  let observing = false;

  const silence = (element: Element) => {
    if (element.getAttribute("aria-live") === "off") return;
    if (observing) ownWrites.set(element, (ownWrites.get(element) ?? 0) + 1);
    element.setAttribute("aria-live", "off");
  };

  const register = (element: Element) => {
    if (!authorLive.has(element)) authorLive.set(element, element.getAttribute("aria-live"));
    const region = classifyLiveRegion(element, authorLive.get(element) ?? null);
    if (!region) {
      regions.delete(element);
      return;
    }
    regions.set(element, region);
    if (region.politeness !== "off") silence(element);
  };

  const registerTree = (node: Node) => {
    if (!(node instanceof Element)) return;
    if (node.matches(LIVE_REGION_SELECTOR)) register(node);
    for (const element of node.querySelectorAll(LIVE_REGION_SELECTOR)) register(element);
  };

  /** The nearest region holding a node, or undefined when none or a silenced one does. */
  const owner = (node: Node): Element | undefined => {
    for (let element = node.parentElement; element; element = element.parentElement) {
      const region = regions.get(element);
      if (region) return region.politeness === "off" ? undefined : element;
      if (element === root) return undefined;
    }
    return undefined;
  };

  const handle = (records: MutationRecord[]) => {
    const changed = new Map<Element, string[]>();
    const note = (region: Element, text: string) => {
      const texts = changed.get(region) ?? [];
      texts.push(text);
      changed.set(region, texts);
    };
    for (const record of records) {
      if (record.type === "attributes") {
        const element = record.target as Element;
        if (record.attributeName === "aria-live") {
          const pending = ownWrites.get(element) ?? 0;
          if (pending > 0) {
            ownWrites.set(element, pending - 1);
            continue;
          }
          authorLive.set(element, element.getAttribute("aria-live"));
        }
        register(element);
        continue;
      }
      if (record.type === "characterData") {
        const region = owner(record.target);
        if (region && !hiddenBelow(record.target, region)) note(region, (record.target as Text).data);
        continue;
      }
      for (const node of record.addedNodes) {
        registerTree(node);
        const inserted = node instanceof Element ? regions.get(node) : undefined;
        if (inserted) {
          if (inserted.politeness === "assertive") note(node as Element, readableText(node));
          continue;
        }
        const region = owner(node);
        if (region && !hiddenBelow(node, region)) note(region, readableText(node));
      }
    }
    for (const [element, texts] of changed) {
      const region = regions.get(element);
      if (!region || region.politeness === "off" || !element.isConnected || concealed(element)) continue;
      const text = normalized(region.atomic ? readableText(element) : texts.join(" "));
      if (text) announce(text, region.politeness);
    }
  };

  registerTree(root);
  const observer = new MutationObserver(handle);
  observer.observe(root, {
    subtree: true,
    childList: true,
    characterData: true,
    attributes: true,
    attributeFilter: ["aria-live", "role", "aria-atomic"],
  });
  observing = true;
  return () => observer.disconnect();
}

/** Routes the page's live regions through the host where it is the announcement channel. */
export function setupLiveRegionBridge(): void {
  if (!hostSpeaksAnnouncements()) return;
  installLiveRegionBridge(document.documentElement, (text, politeness) => {
    void announceThroughHost(text, politeness);
  });
}
