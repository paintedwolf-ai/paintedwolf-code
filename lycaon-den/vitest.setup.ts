import { afterEach, beforeEach, vi } from "vitest";
import { configure } from "@solidjs/testing-library";
import { isAllowlistedConsoleNoise } from "./src/test/console-trap.ts";
import { seedStockFrame } from "./src/contributions/stock-frame-test.ts";
import { setContributionRetrySleepForTest } from "./src/contributions/contribution-store.ts";
import { VITEST_ASYNC_TIMEOUT_MS } from "./src/test/vitest-timeouts.ts";
import { resetSurfaceQueriesForTests } from "./src/ui/surface-query.ts";
import { testHostInfo } from "./src/platform/connection/host-identity-test.ts";
import { resetHostIdentityForTest } from "./src/platform/connection/host-identity.ts";
import { setPreflightReport } from "./src/platform/persistence/preflight-report.ts";

configure({ asyncUtilTimeout: VITEST_ASYNC_TIMEOUT_MS });

/** Supply zero geometry for editor range measurements. */
if (typeof Range !== "undefined") {
  const emptyRect = (): DOMRect =>
    ({
      x: 0,
      y: 0,
      left: 0,
      top: 0,
      right: 0,
      bottom: 0,
      width: 0,
      height: 0,
      toJSON: () => ({}),
    }) as DOMRect;
  if (typeof Range.prototype.getClientRects !== "function") {
    Range.prototype.getClientRects = function getClientRects() {
      return [] as unknown as DOMRectList;
    };
  }
  if (typeof Range.prototype.getBoundingClientRect !== "function") {
    Range.prototype.getBoundingClientRect = function getBoundingClientRect() {
      return emptyRect();
    };
  }
}

/** Supply deterministic canvas text measurements. */
if (typeof HTMLCanvasElement !== "undefined") {
  HTMLCanvasElement.prototype.getContext = vi.fn(
    () =>
      ({
        measureText: vi.fn(() => ({ width: 100 })),
        font: "",
      }) as unknown as CanvasRenderingContext2D,
  ) as typeof HTMLCanvasElement.prototype.getContext;
}

/** Install isolated storage for persistence tests. */
function installLocalStorageMock(): void {
  const store = new Map<string, string>();
  const storage: Storage = {
    get length() {
      return store.size;
    },
    clear() {
      store.clear();
    },
    getItem(key) {
      return store.has(key) ? store.get(key)! : null;
    },
    key(i) {
      return Array.from(store.keys())[i] ?? null;
    },
    removeItem(key) {
      store.delete(key);
    },
    setItem(key, value) {
      store.set(key, String(value));
    },
  };
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: storage,
  });
}

installLocalStorageMock();
beforeEach(() => {
  installLocalStorageMock();
  resetSurfaceQueriesForTests();
  // Seed the shipped contribution frame for each test.
  seedStockFrame();
  // Seed the handshake a loopback host gives its owner.
  resetHostIdentityForTest(testHostInfo());
  // Keep hydration retries synchronous by default.
  setContributionRetrySleepForTest(() => Promise.resolve());
  setPreflightReport({
    overall: "ok",
    probes: [],
    attachment_capabilities: {
      auto_attach_paste_bytes: 16 * 1024,
      max_inline_text_bytes: 64 * 1024,
      max_attachments: 8,
      max_references: 64,
      max_images: 4,
      max_upload_bytes: 16 * 1024 * 1024,
      max_image_bytes: 4 * 1024 * 1024,
      max_body_bytes: 16 * 1024 * 1024,
      max_turn_bytes: 32 * 1024 * 1024,
      max_body_preview_bytes: 16 * 1024,
      max_large_text_preview_bytes: 8 * 1024,
      max_turn_preview_bytes: 32 * 1024,
      max_document_bytes: 16 * 1024 * 1024,
      image_mime_types: ["image/png", "image/jpeg", "image/gif", "image/webp"],
      max_video_bytes: 128 * 1024 * 1024,
      video_mime_types: ["video/mp4", "video/quicktime", "video/webm"],
      text_mime_types: [
        "text/plain",
        "text/markdown",
        "text/csv",
        "application/json",
        "application/yaml",
      ],
      text_extensions: [".go", ".ts", ".tsx", ".js", ".txt", ".md", ".csv", ".json", ".yaml", ".svg"],
      text_basenames: ["dockerfile", "makefile"],
    },
  });
});

/** ResizeObserver is absent in the test DOM. */
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class ResizeObserver {
    constructor(_cb: ResizeObserverCallback) {}
    observe() {}
    unobserve() {}
    disconnect() {}
  } as typeof ResizeObserver;
}

/** Supply reveal behavior absent from the test DOM. */
if (
  typeof Element !== "undefined" &&
  typeof Element.prototype.scrollIntoView !== "function"
) {
  Element.prototype.scrollIntoView = function scrollIntoView() {};
}

/** Fail tests on unexpected console errors or warnings. */
const trappedConsoleErrors: unknown[][] = [];
const trappedConsoleWarns: unknown[][] = [];
const originalConsoleError = console.error;
const originalConsoleWarn = console.warn;
console.error = (...args: unknown[]) => {
  trappedConsoleErrors.push([...args, new Error("Console error origin")]);
  originalConsoleError.apply(console, args as Parameters<typeof console.error>);
};
console.warn = (...args: unknown[]) => {
  trappedConsoleWarns.push([...args, new Error("Console warning origin")]);
  originalConsoleWarn.apply(console, args as Parameters<typeof console.warn>);
};

beforeEach(() => {
  trappedConsoleErrors.length = 0;
  trappedConsoleWarns.length = 0;
});

afterEach(() => {
  const allTrapped = [...trappedConsoleErrors, ...trappedConsoleWarns].filter(
    (args) => !isAllowlistedConsoleNoise(args),
  );
  if (allTrapped.length === 0) return;
  const detail = allTrapped
    .map((args) =>
      args
        .map((a) => (a instanceof Error ? `${a.message}\n${a.stack}` : String(a)))
        .join(" "),
    )
    .join("\n---\n");
  throw new Error(
    `Test produced ${allTrapped.length} unexpected console event(s).\n\n${detail}`,
  );
});
