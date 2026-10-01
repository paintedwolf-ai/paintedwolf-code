import { describe, expect, it, vi } from "vitest";
import { filePathActionItems } from "./project-files-info-actions.ts";
import { resetHostIdentityForTest } from "../../platform/connection/host-identity.ts";
import { testHostInfo } from "../../platform/connection/host-identity-test.ts";

describe("shared file actions", () => {
  it.each(["files-image", "files-info"])("offers destination and path actions for %s", (prefix) => {
    const handlers = { openIn: { absolutePath: "/repo/image.png", projectRoots: ["/repo"], entryKind: "file" as const },
      copyPath: vi.fn(), copyRelativePath: vi.fn(), addToChat: vi.fn() };
    const items = filePathActionItems(handlers, prefix);
    expect(items[0]?.submenu?.map((item) => item.testId)).toEqual(["open-in-file-manager", "open-in-editor", "open-in-browser", "open-in-default-application"]);
    for (const item of items) item.onSelect?.();
    expect(handlers.copyPath).toHaveBeenCalledOnce();
    expect(handlers.copyRelativePath).toHaveBeenCalledOnce();
    expect(handlers.addToChat).toHaveBeenCalledOnce();
  });
  it("omits local destinations when the host does not share the device", () => {
    resetHostIdentityForTest(testHostInfo({ capabilities: [] }));
    const items = filePathActionItems({ openIn: { absolutePath: "/repo/a", projectRoots: ["/repo"], entryKind: "file" }, copyPath: vi.fn(), addToChat: vi.fn() }, "files-info");
    expect(items.map((item) => item.testId)).toEqual(["files-info-copy-path", "files-info-add-to-chat"]);
  });
});
