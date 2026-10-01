import { describe, expect, it } from "vitest";
import { fileBufferKey } from "../components/project-files-model.ts";
import {
  FilesBufferExclusiveRequests,
  FilesBufferLatestRequests,
  LatestOperation,
} from "./files-buffer-request-tracking.ts";

describe("files buffer request tracking", () => {
  const key = fileBufferKey("root-1", "src/main.ts");

  it("coalesces a second load while the first is in flight", () => {
    const requests = new FilesBufferExclusiveRequests();
    const opening = {};
    const first = requests.begin(key, opening);
    expect(first).not.toBeNull();
    if (first == null) return;
    expect(requests.begin(key, opening)).toBeNull();
    expect(requests.isCurrent(key, first)).toBe(true);

    requests.finish(key, first);
    const next = requests.begin(key, opening);
    expect(next).not.toBeNull();
    if (next == null) return;
    expect(next).toBeGreaterThan(first);
  });

  it("admits a reopened buffer while the previous opening is still joining", () => {
    const requests = new FilesBufferExclusiveRequests();
    const first = requests.begin(key, {})!;
    const reopened = {};
    const next = requests.begin(key, reopened)!;
    expect(next).toBeGreaterThan(first);
    expect(requests.isCurrent(key, first)).toBe(false);
    requests.finish(key, first);
    expect(requests.isCurrent(key, next)).toBe(true);
    expect(requests.begin(key, reopened)).toBeNull();
  });

  it("makes an older refresh stale when a newer one starts", () => {
    const requests = new FilesBufferLatestRequests();
    const first = requests.begin(key);
    const second = requests.begin(key);
    expect(requests.isCurrent(key, first)).toBe(false);
    expect(requests.isCurrent(key, second)).toBe(true);
    requests.finish(key, first);
    expect(requests.isCurrent(key, second)).toBe(true);
    requests.finish(key, second);
    expect(requests.isCurrent(key, second)).toBe(false);
  });

  it("never reuses a generation after the current request finishes", () => {
    const requests = new FilesBufferLatestRequests();
    const first = requests.begin(key);
    requests.finish(key, first);
    const second = requests.begin(key);
    expect(second).toBeGreaterThan(first);
    expect(requests.isCurrent(key, first)).toBe(false);
  });
});

describe("latest operation", () => {
  it("keeps stale completions from settling a newer operation", () => {
    const operations = new LatestOperation();
    const first = operations.begin();
    const second = operations.begin();

    expect(operations.isCurrent(first)).toBe(false);
    operations.finish(first);
    expect(operations.isCurrent(second)).toBe(true);
  });

  it("invalidates an operation when its target leaves", () => {
    const operations = new LatestOperation();
    const current = operations.begin();
    operations.invalidate();
    expect(operations.isCurrent(current)).toBe(false);
  });
});
