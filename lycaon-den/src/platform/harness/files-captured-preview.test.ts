// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { openFilesCapturedPreview } from "./files-captured-preview.ts";
import { openFilesBuffer } from "../../files/documents/project-files-buffers.ts";

vi.mock("../../files/documents/project-files-buffers.ts", () => ({ openFilesBuffer: vi.fn(() => "preview") }));
const target = (path: string) => ({ rootId: "remote-fixture", rootLabel: "staging-01", path });

beforeEach(() => {
  vi.stubEnv("VITE_LYCAON_PROXY", "1");
  window.history.replaceState(null, "", "/?files-location-preview=1");
  vi.mocked(openFilesBuffer).mockClear();
});
afterEach(() => { vi.unstubAllEnvs(); window.history.replaceState(null, "", "/"); });

describe("remote capture design fixture", () => {
  it("cannot intercept production or an ordinary harness session", () => {
    vi.stubEnv("VITE_LYCAON_PROXY", "0");
    expect(openFilesCapturedPreview("p", target("etc/nginx/nginx.conf"))).toBe(false);
    vi.stubEnv("VITE_LYCAON_PROXY", "1");
    window.history.replaceState(null, "", "/");
    expect(openFilesCapturedPreview("p", target("etc/nginx/nginx.conf"))).toBe(false);
    expect(openFilesBuffer).not.toHaveBeenCalled();
  });
  it("opens a captured pair in the existing read-only comparison surface", () => {
    expect(openFilesCapturedPreview("p", target("etc/nginx/nginx.conf"))).toBe(true);
    expect(openFilesBuffer).toHaveBeenCalledWith("p", expect.objectContaining({ kind: "diff", diffPreview: expect.objectContaining({ version: expect.objectContaining({ initialComparison: "before", beforeAvailability: "available", availability: "available" }) }) }));
  });
  it("does not invent an absent before side for a partial capture", () => {
    openFilesCapturedPreview("p", target("etc/nginx/upstream.conf"));
    expect(openFilesBuffer).toHaveBeenCalledWith("p", expect.objectContaining({ diffPreview: expect.objectContaining({ version: expect.objectContaining({ initialComparison: "current", beforeAvailability: "not_captured", availability: "available", op: undefined }) }) }));
  });
  it("keeps a report without bytes unavailable rather than showing an empty file", () => {
    openFilesCapturedPreview("p", target("etc/nginx/mime.types"));
    expect(openFilesBuffer).toHaveBeenCalledWith("p", expect.objectContaining({ diffPreview: expect.objectContaining({ notice: expect.objectContaining({ tone: "info" }), version: expect.objectContaining({ source: { kind: "unavailable" }, availability: "not_captured" }) }) }));
  });
  it("leaves outside-project local files on the normal editable path", () => {
    expect(openFilesCapturedPreview("p", { rootId: "local-ops", rootLabel: "ops", path: "deploy.yaml" })).toBe(false);
    expect(openFilesBuffer).not.toHaveBeenCalled();
  });
  it("leaves ordinary project files on their normal path", () => {
    expect(openFilesCapturedPreview("p", { rootId: "local", rootLabel: "website", path: "src/health.ts" })).toBe(false);
    expect(openFilesBuffer).not.toHaveBeenCalled();
  });
});
