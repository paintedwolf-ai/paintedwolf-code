import { describe, expect, it, vi } from "vitest";

vi.mock("../settings/appearance/appearance-prefs.ts", () => ({
  activeThemePaintKey: () => "paint:test",
  onActiveThemeChange: () => () => undefined,
}));
import type { ProjectSummary } from "../project/project-summary.ts";
import { productCoverRef, resolveProjectCardThumbSrc } from "./project-cover-src.ts";
import { recordProjectThumbnail, forgetProjectThumbnail } from "./thumbnail-store.ts";

function summary(over: Partial<ProjectSummary> & { id: string }): ProjectSummary {
  return {
    id: over.id,
    displayName: over.displayName ?? "P",
    folders: over.folders ?? [],
    primaryFolder: over.primaryFolder ?? null,
    folderLabel: over.folderLabel ?? "No folder",
    sessionCount: over.sessionCount ?? 0,
    chatCountLabel: over.chatCountLabel ?? "0 chats",
    lastActivityLabel: over.lastActivityLabel ?? "new",
    lastActivityAtMs: over.lastActivityAtMs ?? null,
    starred: over.starred ?? false,
    isDraft: over.isDraft ?? false,
    coverArtifactId: over.coverArtifactId ?? null,
    coverRootSessionId: over.coverRootSessionId ?? null,
  };
}

describe("project cover source hierarchy", () => {
  it("prefers product-render cover over client Den-view snapshot", () => {
    const p = summary({
      id: "proj-cover",
      coverArtifactId: "art-1",
      coverRootSessionId: "sess-1",
    });
    recordProjectThumbnail(p.id, "data:image/jpeg;base64,client");
    expect(resolveProjectCardThumbSrc(p, "blob:product")).toBe("blob:product");
    forgetProjectThumbnail(p.id);
  });

  it("falls back to client Den-view snapshot when no product cover url", () => {
    const p = summary({ id: "proj-fallback" });
    recordProjectThumbnail(p.id, "data:image/jpeg;base64,client");
    expect(resolveProjectCardThumbSrc(p, null)).toBe("data:image/jpeg;base64,client");
    forgetProjectThumbnail(p.id);
  });

  it("returns null for placeholder when neither cover nor client snapshot", () => {
    const p = summary({ id: "proj-empty" });
    forgetProjectThumbnail(p.id);
    expect(resolveProjectCardThumbSrc(p, null)).toBeNull();
  });

  it("builds a cover ref only when both DTO fields are present", () => {
    expect(
      productCoverRef(
        summary({ id: "a", coverArtifactId: "art", coverRootSessionId: "sess" }),
      ),
    ).toEqual({ sessionId: "sess", artifactId: "art" });
    expect(productCoverRef(summary({ id: "b", coverArtifactId: "art" }))).toBeNull();
  });
});
