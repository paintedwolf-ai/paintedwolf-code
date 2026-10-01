import { denScrollDebugLog, isStreamScrollDebugEnabled } from "../chat/stream/den-scroll-debug.ts";

const RECORD_MS = 30_000;
const FINE_TICK_MS = 250;
const FINE_UNTIL_MS = 4_000;
const COARSE_TICK_MS = 1_000;

/** Specific regions precede their enclosing regions. */
const REGIONS: ReadonlyArray<readonly [string, string]> = [
  ["veil", ".den-shell-workspace-veil"],
  ["chips", '[data-testid="composer-status-chips"]'],
  ["chat-list", '[data-testid="focused-session-list"]'],
  ["nav", "nav.den-shell-nav"],
  ["summary", '[data-testid="files-root-summary"]'],
  ["tree", ".den-files-tree"],
  ["lens", '[data-testid="review-lens"]'],
  ["editor", ".cm-editor"],
  ["tabs", ".tabs"],
  ["launcher", ".den-session-launcher"],
  ["stream", ".den-chat-stream"],
];

function regionOf(node: Node): string {
  const element = node instanceof Element ? node : node.parentElement;
  if (!element) return "other";
  for (const [name, selector] of REGIONS) {
    if (element.closest(selector)) return name;
  }
  return "other";
}

function describe(node: Node): string {
  if (!(node instanceof Element)) return node.nodeName;
  const id = node.getAttribute("data-testid");
  const classes = [...node.classList].slice(0, 2).join(".");
  return `${node.tagName.toLowerCase()}${id ? "#" + id : ""}${classes ? "." + classes : ""}`;
}

/** Samples opening mutations and readiness while scroll debugging is enabled. */
export function recordWorkspaceOpen(key: string, revealed: () => boolean): () => void {
  if (!isStreamScrollDebugEnabled() || typeof MutationObserver !== "function") return () => undefined;
  const started = performance.now();
  const elapsed = () => Math.round(performance.now() - started);
  const counts = new Map<string, number>();
  const samples = new Map<string, string>();
  const firstContent = new Map<string, number>();
  let revealedAt: number | undefined;
  let bucketStart = 0;

  const noteFirstContent = () => {
    for (const [name, selector] of REGIONS) {
      if (firstContent.has(name)) continue;
      const element = document.querySelector<HTMLElement>(selector);
      if (element && (element.textContent ?? "").trim() !== "") firstContent.set(name, elapsed());
    }
  };

  const flush = (event: string) => {
    if (counts.size > 0) {
      const regions = [...counts].sort((a, b) => b[1] - a[1]).map(([name, n]) => `${name}=${n}`).join(" ");
      denScrollDebugLog("perf", event, {
        key,
        from_ms: bucketStart,
        to_ms: elapsed(),
        revealed: revealedAt !== undefined,
        regions,
        samples: [...samples].map(([name, sample]) => `${name}: ${sample}`).join(" | "),
      });
    }
    counts.clear();
    samples.clear();
    bucketStart = elapsed();
  };

  const observer = new MutationObserver((records) => {
    for (const record of records) {
      if (record.type === "childList" && record.addedNodes.length === 0 && record.removedNodes.length === 0) continue;
      const region = regionOf(record.target);
      counts.set(region, (counts.get(region) ?? 0) + 1);
      if (!samples.has(region)) {
        const added = record.addedNodes[0];
        samples.set(region, record.type === "childList" && added ? `${describe(added)} in ${describe(record.target)}` : describe(record.target));
      }
    }
  });
  observer.observe(document.body, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["class", "data-presentation", "data-boot", "aria-busy"],
  });

  let timer: ReturnType<typeof setTimeout> | undefined;
  const tick = () => {
    noteFirstContent();
    if (revealedAt === undefined && revealed()) {
      revealedAt = elapsed();
      // This sample can include mutations after the reveal.
      flush("open-under-veil");
      denScrollDebugLog("perf", "open-reveal", { key, at_ms: revealedAt });
    } else {
      flush("open-timeline");
    }
    if (elapsed() >= RECORD_MS) {
      stop();
      return;
    }
    timer = setTimeout(tick, elapsed() < FINE_UNTIL_MS ? FINE_TICK_MS : COARSE_TICK_MS);
  };
  timer = setTimeout(tick, FINE_TICK_MS);

  const stop = () => {
    clearTimeout(timer);
    observer.disconnect();
    noteFirstContent();
    flush("open-timeline");
    denScrollDebugLog("perf", "open-summary", {
      key,
      revealed_at_ms: revealedAt,
      first_content: [...firstContent].map(([name, at]) => `${name}@${at}`).join(" "),
      recorded_ms: elapsed(),
    });
  };
  return stop;
}
