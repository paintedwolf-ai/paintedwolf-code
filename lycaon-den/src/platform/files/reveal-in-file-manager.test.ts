import { describe, expect, it, vi } from "vitest";
import {
  isAbsolutePath,
  normalizePathForJail,
  pathIsUnderProjectRoots,
  revealInFileManager,
  revealTypedPathInFileManager,
} from "./reveal-in-file-manager.ts";

describe("path jail", () => {
  it("accepts absolute posix paths under a root", () => {
    expect(isAbsolutePath("/proj/a", "linux")).toBe(true);
    expect(isAbsolutePath("rel/a", "linux")).toBe(false);
    expect(
      pathIsUnderProjectRoots("/proj/src/main.go", ["/proj"], "linux"),
    ).toBe(true);
    expect(pathIsUnderProjectRoots("/proj", ["/proj"], "macos")).toBe(true);
    expect(
      pathIsUnderProjectRoots("/other/src", ["/proj"], "macos"),
    ).toBe(false);
    expect(
      pathIsUnderProjectRoots("/proj-evil/x", ["/proj"], "linux"),
    ).toBe(false);
  });

  it("accepts Windows drive and UNC roots", () => {
    expect(isAbsolutePath("C:\\Users\\a", "windows")).toBe(true);
    expect(isAbsolutePath("c:/Users/a", "windows")).toBe(true);
    expect(isAbsolutePath("Users\\a", "windows")).toBe(false);
    expect(
      pathIsUnderProjectRoots(
        "C:\\Users\\me\\proj\\file.go",
        ["C:\\Users\\me\\proj"],
        "windows",
      ),
    ).toBe(true);
    expect(
      pathIsUnderProjectRoots(
        "c:\\users\\me\\proj\\file.go",
        ["C:\\Users\\me\\proj"],
        "windows",
      ),
    ).toBe(true);
    expect(
      pathIsUnderProjectRoots(
        "D:\\elsewhere\\x",
        ["C:\\Users\\me\\proj"],
        "windows",
      ),
    ).toBe(false);
  });

  it("normalizes . and .. for jail comparison", () => {
    expect(normalizePathForJail("/proj/./src/../src/a", "linux")).toBe(
      "/proj/src/a",
    );
    expect(
      pathIsUnderProjectRoots("/proj/./src/../src/a", ["/proj"], "linux"),
    ).toBe(true);
  });
});

describe("revealInFileManager", () => {
  const roots = ["/Users/me/proj"];

  it("rejects empty, relative, and outside-jail paths without spawning", async () => {
    const invokeReveal = vi.fn();

    expect(
      await revealInFileManager("", roots, {
        platform: "macos",
        isTauri: true,
        invokeReveal,
      }),
    ).toEqual({ status: "rejected", reason: "empty_path" });

    expect(
      await revealInFileManager("rel/path", roots, {
        platform: "macos",
        isTauri: true,
        invokeReveal,
      }),
    ).toEqual({ status: "rejected", reason: "not_absolute" });

    expect(
      await revealInFileManager("/elsewhere/x", roots, {
        platform: "macos",
        isTauri: true,
        invokeReveal,
      }),
    ).toEqual({ status: "rejected", reason: "outside_jail" });

    expect(invokeReveal).not.toHaveBeenCalled();
  });

  it.each([
    ["macos", "/Users/me/proj/src/a.go"],
    ["windows", "C:\\Users\\me\\proj\\src\\a.go"],
    ["linux", "/Users/me/proj/src/a.go"],
  ] as const)(
    "desktop %s branch invokes Tauri after jail pass",
    async (platform, abs) => {
      const invokeReveal = vi.fn(async () => {});
      const projectRoots =
        platform === "windows" ? ["C:\\Users\\me\\proj"] : roots;

      const result = await revealInFileManager(abs, projectRoots, {
        platform,
        isTauri: true,
        invokeReveal,
      });

      expect(result).toEqual({ status: "revealed" });
      expect(invokeReveal).toHaveBeenCalledWith(abs, projectRoots);
    },
  );

  it("rejects file-manager opening outside the desktop app", async () => {
    const invokeReveal = vi.fn();

    const result = await revealInFileManager("/Users/me/proj/a.go", roots, {
      platform: "macos",
      isTauri: false,
      invokeReveal,
    });

    expect(result).toEqual({ status: "rejected", reason: "desktop_unavailable" });
    expect(invokeReveal).not.toHaveBeenCalled();
  });

  it("maps spawn failure to rejected", async () => {
    const result = await revealInFileManager("/Users/me/proj/a.go", roots, {
      platform: "linux",
      isTauri: true,
      invokeReveal: async () => {
        throw new Error("spawn failed");
      },
    });
    expect(result).toEqual({ status: "rejected", reason: "spawn_failed" });
  });

});

describe("revealTypedPathInFileManager", () => {
  it("reveals an absolute path outside every root without a jail", async () => {
    const invokeReveal = vi.fn(async () => undefined);
    const result = await revealTypedPathInFileManager("/elsewhere/notes.md", { platform: "macos", isTauri: true, invokeReveal });
    expect(result).toEqual({ status: "revealed" });
    expect(invokeReveal).toHaveBeenCalledWith("/elsewhere/notes.md");
  });

  it("rejects relative paths and the browser runtime", async () => {
    expect(await revealTypedPathInFileManager("notes.md", { platform: "macos", isTauri: true }))
      .toEqual({ status: "rejected", reason: "not_absolute" });
    expect(await revealTypedPathInFileManager("C:\\notes.md", { platform: "windows", isTauri: false }))
      .toEqual({ status: "rejected", reason: "desktop_unavailable" });
  });
});
