import { describe, expect, it } from "vitest";
import { peelEvidenceHandleTag } from "./tool-output-handle.ts";

describe("peelEvidenceHandleTag", () => {
  it("strips a read handle before JSON body", () => {
    const body = '{"path":"go.mod","content":"line1\\nline2"}';
    const tagged = `[read#1]\n${body}`;
    expect(peelEvidenceHandleTag(tagged)).toEqual({
      handle: "read#1",
      body,
    });
  });

  it("strips leg-namespaced handles", () => {
    expect(peelEvidenceHandleTag("[leg-a:grep#2]\nmatches")).toEqual({
      handle: "leg-a:grep#2",
      body: "matches",
    });
  });

  it("preserves nested ledger scopes", () => {
    expect(peelEvidenceHandleTag("[root:child:grep#2]\nmatches")).toEqual({
      handle: "root:child:grep#2",
      body: "matches",
    });
  });

  it("leaves untagged output unchanged", () => {
    const plain = '{"path":"a.go","content":"package a\\n"}';
    expect(peelEvidenceHandleTag(plain)).toEqual({
      handle: null,
      body: plain,
    });
  });

  it("strips underscore kinds such as page_geometry", () => {
    const body = '{"unit":"css_px"}';
    expect(peelEvidenceHandleTag(`[page_geometry#1]\n${body}`)).toEqual({
      handle: "page_geometry#1",
      body,
    });
  });
});
