import { describe, expect, it } from "vitest";
import type { SearchHit, SecurityFinding } from "../api/types.ts";
import type { FileBuffer } from "../files/documents/files-buffer-state.ts";
import { filesViewFacet } from "../files/components/files-view-facet.ts";
import { searchViewFacet } from "../components/search/search-view-facet.ts";
import { securityViewFacet } from "../components/scan/security-view-facet.ts";

const buffer = (fields: Partial<FileBuffer>) => ({ rootId: "r", path: "src/a.go", documentId: null, ...fields }) as FileBuffer;

describe("filesViewFacet", () => {
  it("names the editor's document, a diff's file, and the selected folder while the tree is shown", () => {
    expect(filesViewFacet(buffer({ kind: "text", documentId: "doc" }), true, { rootId: "r", path: "src", kind: "folder" }))
      .toEqual({ editor: { document_id: "doc" }, folder: { root_id: "r", path: "src" } });
    expect(filesViewFacet(buffer({ kind: "diff" }), false, { rootId: "r", path: "src", kind: "folder" }))
      .toEqual({ diff: { root_id: "r", path: "src/a.go" } });
  });

  it("leaves out what is not a file editor or a chosen folder", () => {
    expect(filesViewFacet(buffer({ kind: "image" }), true, { rootId: "r", path: "src/a.go", kind: "file" })).toEqual({});
    expect(filesViewFacet(null, true, { rootId: "r", path: ".", kind: "folder" })).toEqual({});
  });
});

describe("searchViewFacet", () => {
  const hit = { project_id: "p", root_id: "r", path: "src/a.go", line: 4 } as SearchHit;
  it("carries the query and a selected hit inside the project", () => {
    expect(searchViewFacet("p", " Token ", hit)).toEqual({ search: { query: "Token", hit: { root_id: "r", path: "src/a.go", line: 4 } } });
    expect(searchViewFacet("q", "Token", hit)).toEqual({ search: { query: "Token", hit: undefined } });
  });
  it("reports no search without a usable query", () => {
    expect(searchViewFacet("p", "  ", hit)).toEqual({});
    expect(searchViewFacet("p", "x".repeat(1025), hit)).toEqual({});
  });
});

describe("securityViewFacet", () => {
  const finding = (uri: string) => ({
    rule_id: "go.sqli", level: "high", message: "m", fingerprints: { primary: "fp" },
    locations: [{ uri, start_line: 9 }], tool: { driver_id: "d", name: "n" },
  }) as SecurityFinding;
  it("locates a relative finding in the scanned folder", () => {
    expect(securityViewFacet("r", finding("src/db.go"))).toEqual({
      findings: [{ fingerprint: "fp", rule_id: "go.sqli", level: "high", location: { root_id: "r", path: "src/db.go", line: 9 } }],
    });
  });
  it("names a finding without a place it can address", () => {
    expect(securityViewFacet("r", finding("file:///etc/passwd")).findings?.[0]?.location).toBeUndefined();
    expect(securityViewFacet("r", null)).toEqual({});
  });
});
