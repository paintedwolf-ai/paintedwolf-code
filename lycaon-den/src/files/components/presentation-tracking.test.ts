import { workingFileFixture } from "../review/review-lens-fixtures.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import {
  completeFilePresentation,
  filePresentationTarget,
  trackFilePresentation,
} from "./presentation-tracking.ts";
import type { LycaonClient } from "../../api/client.ts";

function mockClient(post = vi.fn().mockResolvedValue(undefined)): LycaonClient {
  return stubClient({ completeProjectSourcePresentation: post });
}

describe("presentation tracking", () => {
  it("acknowledges only a valid target whose exact saved bytes were displayed", () => {
    const file = workingFileFixture("a.ts");
    expect(filePresentationTarget(file, file.file_id, "tip-a.ts")).toEqual({
      fileId: file.file_id, effectId: "effect-a.ts", ordinal: 7,
    });
    expect(filePresentationTarget(file, file.file_id, "older-bytes")).toBeNull();
    expect(filePresentationTarget(file, "another-file", "tip-a.ts")).toBeNull();
    expect(filePresentationTarget({ ...file, presentation_effect_id: "" }, file.file_id, "tip-a.ts")).toBeNull();
    expect(filePresentationTarget({ ...file, presentation_ordinal: NaN }, file.file_id, "tip-a.ts")).toBeNull();
    expect(filePresentationTarget({ ...file, tip: { state: "absent" } }, file.file_id, "tip-a.ts")).toBeNull();
  });

  it("completes when the last visible token leaves", () => {
    const complete = vi.fn();
    const target = {
      fileId: "file-a",
      effectId: "effect-1",
      ordinal: 1,
    };
    trackFilePresentation("p1", "win-a:editor", target, complete);
    trackFilePresentation("p1", "win-b:editor", target, complete);
    expect(complete).not.toHaveBeenCalled();

    trackFilePresentation("p1", "win-a:editor", null, complete);
    expect(complete).not.toHaveBeenCalled();

    trackFilePresentation("p1", "win-b:editor", null, complete);
    expect(complete).toHaveBeenCalledTimes(1);
    expect(complete).toHaveBeenCalledWith(target);
  });

  it("completes the previous file when a token switches files", () => {
    const complete = vi.fn();
    const a = {
      fileId: "file-a",
      effectId: "effect-a",
      ordinal: 1,
    };
    const b = {
      fileId: "file-b",
      effectId: "effect-b",
      ordinal: 2,
    };
    trackFilePresentation("p1", "editor", a, complete);
    trackFilePresentation("p1", "editor", b, complete);
    expect(complete).toHaveBeenCalledWith(a);
    trackFilePresentation("p1", "editor", null, complete);
    expect(complete).toHaveBeenLastCalledWith(b);
  });

  it("refreshes the target while the same file stays visible", () => {
    const complete = vi.fn();
    trackFilePresentation(
      "p1",
      "editor",
      { fileId: "file-a", effectId: "effect-1", ordinal: 1 },
      complete,
    );
    trackFilePresentation(
      "p1",
      "editor",
      { fileId: "file-a", effectId: "effect-2", ordinal: 2 },
      complete,
    );
    expect(complete).not.toHaveBeenCalled();
    trackFilePresentation("p1", "editor", null, complete);
    expect(complete).toHaveBeenCalledWith({
      fileId: "file-a",
      effectId: "effect-2",
      ordinal: 2,
    });
  });

  it("completes the final presentation's effect", () => {
    const complete = vi.fn();
    const newer = {
      fileId: "file-a",
      effectId: "effect-2",
      ordinal: 2,
    };
    const older = {
      fileId: "file-a",
      effectId: "effect-1",
      ordinal: 1,
    };
    trackFilePresentation("p1", "newer", newer, complete);
    trackFilePresentation("p1", "older", older, complete);
    trackFilePresentation("p1", "older", null, complete);
    trackFilePresentation("p1", "newer", null, complete);
    expect(complete).toHaveBeenCalledWith(newer);
  });

  it("posts the exact completed presentation", async () => {
    const post = vi.fn().mockResolvedValue(undefined);
    await completeFilePresentation(mockClient(post), "p1", {
      fileId: "file-a",
      effectId: "effect-1",
      ordinal: 3,
    });
    expect(post).toHaveBeenCalledWith("p1", {
      file_id: "file-a",
      effect_id: "effect-1",
      ordinal: 3,
    });
  });

  it("acknowledges the furthest version seen even when an older pane closes last", () => {
    const complete = vi.fn();
    const newer = { fileId: "file-a", effectId: "newer", ordinal: 8 };
    trackFilePresentation("p1", "newer", newer, complete);
    trackFilePresentation("p1", "older", { fileId: "file-a", effectId: "older", ordinal: 5 }, complete);
    trackFilePresentation("p1", "newer", null, complete);
    trackFilePresentation("p1", "older", null, complete);
    expect(complete).toHaveBeenCalledExactlyOnceWith(newer);
  });

  it("keeps the completion bound to the project that was displayed", () => {
    const first = vi.fn(), second = vi.fn();
    const target = { fileId: "file", effectId: "effect", ordinal: 1 };
    trackFilePresentation("first", "reused-token", target, first);
    trackFilePresentation("second", "reused-token", target, second);
    expect(first).toHaveBeenCalledExactlyOnceWith(target);
    expect(second).not.toHaveBeenCalled();
    trackFilePresentation("second", "reused-token", null, second);
    expect(second).toHaveBeenCalledExactlyOnceWith(target);
  });
});
