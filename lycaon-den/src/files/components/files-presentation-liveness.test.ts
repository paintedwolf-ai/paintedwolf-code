import { describe, expect, it, vi } from "vitest";
import {
  filesPresentationLivenessViolation,
  reportFilesPresentationLivenessViolation,
  takeFilesPresentationLivenessViolations,
} from "./files-presentation-liveness.ts";
import { fileBufferKey } from "./project-files-model.ts";

const KEY = fileBufferKey("r1", "b.ts");

describe("files presentation liveness", () => {
  it("accepts an aim backed by a read or by workspace resolution", () => {
    expect(
      filesPresentationLivenessViolation({
        projectId: "p1", presentationPending: true, displayedKey: null, preparing: [], elapsedMs: 300,
        requestedKey: KEY,
        readInFlight: true,
        workspaceResolving: false,
      }),
    ).toBeNull();
    expect(
      filesPresentationLivenessViolation({
        projectId: "p1", presentationPending: true, displayedKey: null, preparing: [], elapsedMs: 300,
        requestedKey: KEY,
        readInFlight: false,
        workspaceResolving: true,
      }),
    ).toBeNull();
    expect(
      filesPresentationLivenessViolation({
        projectId: "p1", presentationPending: true, displayedKey: null, preparing: [], elapsedMs: 300,
        requestedKey: null,
        readInFlight: false,
        workspaceResolving: false,
      }),
    ).toBeNull();
  });

  it("names a pending aim that nothing will ever present", () => {
    expect(
      filesPresentationLivenessViolation({
        projectId: "p1", presentationPending: true, displayedKey: null, preparing: [], elapsedMs: 300,
        requestedKey: KEY,
        readInFlight: false,
        workspaceResolving: false,
      }),
    ).toMatchObject({ projectId: "p1", key: KEY, reason: expect.stringContaining("no read in flight"), preparing: [] });
  });

  it("collects reports for tests and drains them on take", () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      takeFilesPresentationLivenessViolations();
      reportFilesPresentationLivenessViolation({ projectId: "p1", key: KEY, reason: "probe" });
      expect(logged).toHaveBeenCalledTimes(1);
      expect(takeFilesPresentationLivenessViolations()).toHaveLength(1);
      expect(takeFilesPresentationLivenessViolations()).toEqual([]);
    } finally {
      logged.mockRestore();
    }
  });

  it("detects a loaded aim stuck behind presentation even without a pending source read", () => {
    const facts = { projectId: "p1", presentationPending: true, requestedKey: KEY, displayedKey: fileBufferKey("r1", "a.ts"),
      readInFlight: false, workspaceResolving: false, preparing: ["editor viewport"], elapsedMs: 300 };
    expect(filesPresentationLivenessViolation(facts)).toBeNull();
    expect(filesPresentationLivenessViolation({ ...facts, elapsedMs: 10_000 })).toMatchObject({
      key: KEY, preparing: ["editor viewport"], reason: "presentation still pending after 10 seconds",
    });
    expect(filesPresentationLivenessViolation({ ...facts, displayedKey: KEY, presentationPending: false, elapsedMs: 10_000 })).toBeNull();
  });
});
