import { describe, expect, it } from "vitest";
import { classifyMcpHttpUrl, parseMcpHttpUrl } from "./mcp-http-url.ts";

describe("parseMcpHttpUrl", () => {
  it.each([
    ["127.0.0.1:8765", "http://127.0.0.1:8765", "loopback"],
    ["127.0.0.1:8765/mcp", "http://127.0.0.1:8765/mcp", "loopback"],
    ["localhost:8765", "http://localhost:8765", "loopback"],
    ["http://127.0.0.1:8765/mcp", "http://127.0.0.1:8765/mcp", "loopback"],
    ["https://127.0.0.1:8765/mcp", "https://127.0.0.1:8765/mcp", "loopback"],
    ["[::1]:8765", "http://[::1]:8765", "loopback"],
    ["::1", "http://[::1]", "loopback"],
    ["intel.example/mcp", "https://intel.example/mcp", "remote"],
    ["https://intel.example/mcp", "https://intel.example/mcp", "remote"],
    ["http://intel.example/mcp", "http://intel.example/mcp", "remote"],
    ["172.17.0.2:8080/mcp", "https://172.17.0.2:8080/mcp", "remote"],
    ["localhost.evil.com/mcp", "https://localhost.evil.com/mcp", "remote"],
  ] as const)("%s → %s (%s)", (input, url, kind) => {
    expect(parseMcpHttpUrl(input)).toEqual({ ok: true, url, kind });
  });

  it.each(["", "ftp://intel.example/mcp", "http://user:pass@127.0.0.1/mcp", "/mcp"])(
    "rejects %s",
    (input) => {
      const got = parseMcpHttpUrl(input);
      expect(got.ok).toBe(false);
    },
  );
});

describe("classifyMcpHttpUrl", () => {
  it("accepts a schemeless loopback address", () => {
    expect(classifyMcpHttpUrl("127.0.0.1:8765", false)).toEqual({
      ok: true,
      url: "http://127.0.0.1:8765",
      kind: "loopback",
    });
  });

  it("refuses explicit remote HTTP", () => {
    expect(classifyMcpHttpUrl("http://intel.example/mcp", false)).toEqual({
      ok: false,
      code: "remote_requires_https",
    });
  });

  it("refuses a remote URL in project scope", () => {
    expect(classifyMcpHttpUrl("https://intel.example/mcp", true)).toEqual({
      ok: false,
      code: "project_remote_forbidden",
    });
  });
});
