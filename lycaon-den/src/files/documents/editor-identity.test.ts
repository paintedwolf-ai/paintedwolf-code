import { describe, expect, it } from "vitest";
import { editorStateAction, resolveEditorIdentity } from "./editor-identity.ts";

const base = {
  historical: false,
  workerDraft: false,
  deleted: false,
  writable: true as boolean | null,
  preview: false,
  editable: true,
};

describe("editor identity", () => {
  it.each([
    [{ historical: true }, "historical-version", "Restore this version"],
    [{ workerDraft: true }, "worker-draft", "Open current file"],
    [{ deleted: true }, "deleted", "Restore file"],
    [{ writable: false, editable: false }, "read-only-file", "Make editable"],
    [{ preview: true }, "preview", null],
    [{}, "editing", null],
    [{ editable: false }, "editing-error", "Retry editing"],
    [{ editable: false, documentPending: true }, "opening", null],
    [{ editable: false, opening: { status: "opening" } }, "opening", null],
    [{ editable: false, opening: { status: "reconnecting", message: "Offline" } }, "reconnecting", null],
    [{ editable: false, opening: { status: "error", message: "Invalid checkpoint" } }, "editing-error", "Retry editing"],
    [{ editable: false, paused: true }, "editing-paused", null],
    [{ editable: false, overLimit: true }, "large-file", null],
    [{ editable: false, unsupported: true }, "view-only", null],
  ] as const)("resolves %j", (override, identityId, actionLabel) => {
    const identity = resolveEditorIdentity({ ...base, ...override });
    expect(identity.id).toBe(identityId);
    expect(editorStateAction(identity)?.label ?? null).toBe(actionLabel);
  });

  it("uses document identity precedence", () => {
    expect(resolveEditorIdentity({
      ...base,
      historical: true,
      workerDraft: true,
      writable: false,
      preview: true,
    }).id).toBe("historical-version");
    expect(resolveEditorIdentity({
      ...base,
      workerDraft: true,
      writable: false,
      preview: true,
    }).id).toBe("worker-draft");
  });
});
