import { readSourceText } from "../../test/stylesheet-source.ts";

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi, afterEach } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import {
  PROJECT_THUMB_TINT_COUNT,
  ProjectThumbnail,
  tintIndexFor,
} from "./ProjectThumbnail.tsx";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ProjectThumbnail reveal", () => {
  it("stays blank while a cover fetch is pending", () => {
    const { getByTestId } = render(() => (
      <ProjectThumbnail seed="proj-a" pending src={null} />
    ));
    const stack = getByTestId("project-thumbnail");
    expect(stack.getAttribute("data-pending")).toBe("true");
    expect(stack.getAttribute("data-placeholder-ready")).toBeNull();
    expect(stack.getAttribute("data-live-ready")).toBeNull();
    expect(stack.querySelector(".project-thumb--ready")).toBeNull();
  });

  it("fades in the tinted placeholder once settled with no cover", async () => {
    const { getByTestId } = render(() => (
      <ProjectThumbnail seed="proj-b" pending={false} src={null} />
    ));
    const stack = getByTestId("project-thumbnail");
    await waitFor(() => {
      expect(stack.getAttribute("data-placeholder-ready")).toBe("true");
    });
    expect(stack.querySelector(".project-thumb--ready")).toBeTruthy();
  });

  it("fades in a live cover without revealing the placeholder first", async () => {
    const [src, setSrc] = createSignal<string | null>(null);
    const [pending, setPending] = createSignal(true);
    const { getByTestId, queryByTestId } = render(() => (
      <ProjectThumbnail seed="proj-c" pending={pending()} src={src()} />
    ));

    const stack = getByTestId("project-thumbnail");
    expect(stack.getAttribute("data-placeholder-ready")).toBeNull();

    setPending(false);
    setSrc("data:image/gif;base64,R0lGODlhAQABAAAAACwAAAAAAQABAAA=");

    await waitFor(() => {
      expect(queryByTestId("project-thumbnail-live")).toBeTruthy();
    });
    const img = getByTestId("project-thumbnail-live");
    img.dispatchEvent(new Event("load"));

    await waitFor(() => {
      expect(stack.getAttribute("data-live-ready")).toBe("true");
    });
    expect(stack.getAttribute("data-placeholder-ready")).toBeNull();
    expect(stack.querySelector(".project-thumb--ready")).toBeNull();
  });
});

describe("placeholder tint", () => {
  it("buckets a seed deterministically and stays in range", () => {
    for (const seed of ["proj-a", "proj-b", "", "0123456789abcdef"]) {
      const index = tintIndexFor(seed);
      expect(index).toBe(tintIndexFor(seed));
      expect(index).toBeGreaterThanOrEqual(0);
      expect(index).toBeLessThan(PROJECT_THUMB_TINT_COUNT);
    }
  });

  it("distributes project seeds across every available tint", () => {
    const buckets = new Set(
      Array.from({ length: 100 }, (_, i) => tintIndexFor(`project-${i}`)),
    );
    expect(buckets.size).toBe(PROJECT_THUMB_TINT_COUNT);
  });

  it("carries the tint on the placeholder so CSS controls the colors", () => {
    const { getByTestId } = render(() => (
      <ProjectThumbnail seed="proj-b" pending={false} src={null} />
    ));
    const placeholder = getByTestId("project-thumbnail").querySelector(
      ".project-thumb--placeholder",
    );
    expect(placeholder?.getAttribute("data-tint")).toBe(String(tintIndexFor("proj-b")));
    // CSS defines placeholder colors across themes.
    expect(placeholder?.getAttribute("style") ?? "").not.toMatch(/background/);
  });

  it("gives every tint its own themed hue", () => {
    const css = readSourceText(
      join(dirname(fileURLToPath(import.meta.url)), "../../global-components.css"),
      "utf8",
    );
    // Every bucket uses a distinct theme identity token.
    const hues = new Set<string>();
    for (let i = 0; i < PROJECT_THUMB_TINT_COUNT; i += 1) {
      const selector =
        i === 0
          ? /\.project-thumb--placeholder\s*\{([^}]*)\}/
          : new RegExp(
              `\\.project-thumb--placeholder\\[data-tint="${i}"\\]\\s*\\{([^}]*)\\}`,
            );
      const body = selector.exec(css)?.[1];
      expect(body, `tint ${i} has no rule`).toBeTruthy();
      const hue = /--thumb-hue:\s*var\((--den-identity-\d+)\)/.exec(body!)?.[1];
      expect(hue, `tint ${i} does not name an identity token`).toBeTruthy();
      hues.add(hue!);
    }
    expect(hues.size, "tints must be distinguishable at a glance").toBe(
      PROJECT_THUMB_TINT_COUNT,
    );
  });
});
