import { describe, expect, it } from "vitest";
import type { TrustFileChange } from "../../api/types.ts";
import { trustChangeVersion } from "./trust-review-navigation.ts";

const file: TrustFileChange = { id: "file", root_id: "root", root_label: "Root", path: "AGENTS.md", surface_ids: ["agents_md"], kind: "modified", before: "old\r\n", after: "new\r\n" };

describe("trust review comparison", () => {
  it("shows the captured comparison with normalized editor lines", () => {
    expect(trustChangeVersion(file)).toMatchObject({ initialComparison: "before", sizeBytes: 5,
op: "write", source: { kind: "text", before: "old\n", after: "new\n" } });
  });
  it("preserves an empty file's addition and removal identities", () => {
    expect(trustChangeVersion({ ...file, kind: "added", before: "", after: "" })).toMatchObject({ op: "create", beforeAvailability: "absent", availability: "available" });
    expect(trustChangeVersion({ ...file, kind: "removed", before: "", after: "" })).toMatchObject({ op: "delete", beforeAvailability: "available", availability: "absent" });
  });
});
