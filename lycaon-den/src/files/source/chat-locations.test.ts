import { afterEach, describe, expect, it } from "vitest";
import type { SourceChatLocation } from "../../api/types.ts";
import { ledgerAddress, locationLedgerPath, locationRowPath, recordChatLocations, resetChatLocationsForTests, treeAddress } from "./chat-locations.ts";

const folder: SourceChatLocation = { id: "loc-folder", session_id: "chat", root_id: "outside", anchor: "Users/me/site", path: "/Users/me/site", kind: "repository", label: "site", hidden: false };
const file: SourceChatLocation = { id: "loc-file", session_id: "chat", root_id: "outside", anchor: "Users/me/.ssh/config", path: "/Users/me/.ssh/config", kind: "file", label: ".ssh", hidden: false };

describe("chat locations", () => {
  afterEach(resetChatLocationsForTests);

  it("maps folder rows to outside paths and back", () => {
    expect(locationLedgerPath(folder, ".")).toBe("Users/me/site");
    expect(locationLedgerPath(folder, "src/app.ts")).toBe("Users/me/site/src/app.ts");
    expect(locationRowPath(folder, "Users/me/site/src/app.ts")).toBe("src/app.ts");
    expect(locationRowPath(folder, "Users/me/site")).toBe(".");
    expect(locationRowPath(folder, "Users/me/sites/other")).toBeUndefined();
  });

  it("lists a file location as its one file", () => {
    expect(locationLedgerPath(file, "config")).toBe("Users/me/.ssh/config");
    expect(locationLedgerPath(file, ".")).toBe("Users/me/.ssh");
    expect(locationRowPath(file, "Users/me/.ssh/config")).toBe("config");
    expect(locationRowPath(file, "Users/me/.ssh/known_hosts")).toBeUndefined();
  });

  it("translates between tree rows and source history addresses", () => {
    recordChatLocations("project", [folder, file]);
    expect(ledgerAddress("project", "outside", "loc-folder", "index.html")).toEqual({ rootId: "outside", path: "Users/me/site/index.html" });
    expect(ledgerAddress("project", "outside", "project-root", "main.go")).toEqual({ rootId: "project-root", path: "main.go" });
    expect(treeAddress("project", "outside", "outside", "Users/me/.ssh/config")).toEqual({ rootId: "loc-file", path: "config" });
    expect(treeAddress("project", "outside", "outside", "etc/hosts")).toBeUndefined();
    expect(treeAddress("project", "outside", "project-root", "main.go")).toEqual({ rootId: "project-root", path: "main.go" });
  });
});
