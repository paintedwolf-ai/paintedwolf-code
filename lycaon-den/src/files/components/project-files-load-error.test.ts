import { beforeEach, describe, expect, it } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { applyProjectSourceLoadFailure } from "./project-files-load-error.ts";

const PROJECT = "p1";

describe("applyProjectSourceLoadFailure", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
  });

  it("opens the unsupported-encoding info card from details.detected", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "wide.txt",
    });
    applyProjectSourceLoadFailure(
      PROJECT,
      key,
      new LycaonApiError("refused", 415, "unsupported_encoding", {
        details: { detected: "unknown" },
      }),
    );
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.kind).toBe("info");
    expect(buf.unsupportedEncodingDetected).toBe("unknown");
    expect(buf.loadError).toBeNull();
  });

  it("keeps ordinary failures as loadError strings", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "missing.txt",
    });
    applyProjectSourceLoadFailure(
      PROJECT,
      key,
      new LycaonApiError("file not found", 404, "source_not_found"),
    );
    expect(projectFilesState(PROJECT).byKey[key]!.loadError).toContain(
      "file not found",
    );
  });
});
