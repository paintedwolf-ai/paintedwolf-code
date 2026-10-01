import { describe, expect, it } from "vitest";
import { isBackendReachable, isSidecarEstablished } from "./sidecar-status.ts";

describe("sidecar-status", () => {
  it("isSidecarEstablished after handshake only", () => {
    expect(isSidecarEstablished("connected")).toBe(true);
    expect(isSidecarEstablished("reconnecting")).toBe(true);
    expect(isSidecarEstablished("connecting")).toBe(false);
    expect(isSidecarEstablished("disconnected")).toBe(false);
  });

  it("isBackendReachable during connecting and reconnecting SSE handshakes", () => {
    expect(isBackendReachable("connecting")).toBe(true);
    expect(isBackendReachable("reconnecting")).toBe(true);
    expect(isBackendReachable("connected")).toBe(true);
    expect(isBackendReachable("disconnected")).toBe(false);
  });
});
