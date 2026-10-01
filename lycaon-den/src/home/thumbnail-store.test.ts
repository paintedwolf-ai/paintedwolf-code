// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

const themeRuntime = vi.hoisted(() => ({
  paintKey: "paint:initial",
  listener: null as ((paintKey: string) => void) | null,
}));

vi.mock("../settings/appearance/appearance-prefs.ts", () => ({
  activeThemePaintKey: () => themeRuntime.paintKey,
  onActiveThemeChange: (listener: (paintKey: string) => void) => {
    themeRuntime.listener = listener;
    return () => {
      if (themeRuntime.listener === listener) themeRuntime.listener = null;
    };
  },
}));
import {
  forgetAllProjectThumbnails,
  forgetProjectThumbnail,
  projectThumbnail,
  recordProjectThumbnail,
  setupProjectThumbnailThemeInvalidation,
} from "./thumbnail-store.ts";

describe("thumbnail-store theme invalidation", () => {
  afterEach(() => {
    forgetAllProjectThumbnails();
    themeRuntime.paintKey = "paint:reset";
    themeRuntime.listener = null;
    localStorage.clear();
  });

  it("forgetAllProjectThumbnails clears every client snapshot", () => {
    recordProjectThumbnail("a", "data:a");
    recordProjectThumbnail("b", "data:b");
    forgetAllProjectThumbnails();
    expect(projectThumbnail("a")).toBeNull();
    expect(projectThumbnail("b")).toBeNull();
  });

  it("drops snapshots when the applied palette changes", () => {
    themeRuntime.paintKey = "paint:light";
    recordProjectThumbnail("chat", "data:image/jpeg;base64,light");
    const stop = setupProjectThumbnailThemeInvalidation();
    expect(projectThumbnail("chat")).toBe("data:image/jpeg;base64,light");

    themeRuntime.paintKey = "paint:dark";
    themeRuntime.listener?.("paint:dark");
    expect(projectThumbnail("chat")).toBeNull();

    recordProjectThumbnail("chat", "data:image/jpeg;base64,dark");
    themeRuntime.listener?.("paint:dark");
    expect(projectThumbnail("chat")).toBe("data:image/jpeg;base64,dark");

    themeRuntime.paintKey = "paint:other-dark-theme";
    themeRuntime.listener?.("paint:other-dark-theme");
    expect(projectThumbnail("chat")).toBeNull();
    stop();
  });

  it("rejects a capture that completed after the palette changed", () => {
    themeRuntime.paintKey = "paint:before";
    themeRuntime.paintKey = "paint:after";

    recordProjectThumbnail("chat", "data:stale", "paint:before");

    expect(projectThumbnail("chat")).toBeNull();
  });

  it("persists pixels and their paint identity in one record", () => {
    themeRuntime.paintKey = "paint:atomic";
    recordProjectThumbnail("chat", "data:atomic");

    const persisted = JSON.parse(
      localStorage.getItem("den.project-thumbnails.v1") ?? "null",
    ) as {
      paintKey?: string;
      thumbnails?: Record<string, { dataUrl?: string }>;
    } | null;
    expect(persisted?.paintKey).toBe("paint:atomic");
    expect(persisted?.thumbnails?.chat?.dataUrl).toBe("data:atomic");
    expect(localStorage.length).toBe(1);
  });

  it("forgetProjectThumbnail still drops a single id", () => {
    recordProjectThumbnail("keep", "data:keep");
    recordProjectThumbnail("drop", "data:drop");
    forgetProjectThumbnail("drop");
    expect(projectThumbnail("keep")).toBe("data:keep");
    expect(projectThumbnail("drop")).toBeNull();
  });
});
