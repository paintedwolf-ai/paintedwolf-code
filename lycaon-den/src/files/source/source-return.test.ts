import { afterEach, describe, expect, it, vi } from "vitest";
import { clearSourceReturn, rememberSourceReturn, returnToSource, sourceReturn } from "./source-return.ts";
import { revealFilesBuffer } from "../documents/project-files-buffers.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
vi.mock("../documents/project-files-buffers.ts", () => ({ revealFilesBuffer: vi.fn() }));
vi.mock("../../platform/navigation/open-source.ts", () => ({ openSourceLocation: vi.fn() }));
const origin = { projectId: "project", bufferKey: "walk:step", rootId: "root", path: "load.go", jobId: "worker", line: 25 };
afterEach(() => { clearSourceReturn(); vi.resetAllMocks(); });
describe("return from line facts", () => {
  it("reveals the original tab and line without replacing a historical presentation", async () => {
    vi.mocked(revealFilesBuffer).mockReturnValue(true);
    rememberSourceReturn(origin);
    expect(await returnToSource()).toBe(true);
    expect(revealFilesBuffer).toHaveBeenCalledWith("project", "walk:step", 25);
    expect(openSourceLocation).not.toHaveBeenCalled();
    expect(sourceReturn()).toBeNull();
  });
  it("reopens a closed tab in-app and retains the return when opening fails", async () => {
    vi.mocked(revealFilesBuffer).mockReturnValue(false);
    vi.mocked(openSourceLocation).mockResolvedValue({ status: "rejected", reason: "Unavailable" });
    rememberSourceReturn(origin);
    expect(await returnToSource()).toBe(false);
    expect(openSourceLocation).toHaveBeenCalledWith({ ...origin, intent: "transient" });
    expect(sourceReturn()).toEqual(origin);
    vi.mocked(openSourceLocation).mockResolvedValue({ status: "opened-in-app" });
    expect(await returnToSource()).toBe(true);
    expect(sourceReturn()).toBeNull();
  });
  it("does not clear a newer origin when an older open settles", async () => {
    vi.mocked(revealFilesBuffer).mockReturnValue(false);
    let finish!: (result: { status: "opened-in-app" }) => void;
    vi.mocked(openSourceLocation).mockReturnValue(new Promise(resolve => { finish = resolve; }));
    rememberSourceReturn(origin);
    const opening = returnToSource();
    rememberSourceReturn({ ...origin, line: 40 });
    finish({ status: "opened-in-app" });
    await opening;
    expect(sourceReturn()?.line).toBe(40);
  });
});
