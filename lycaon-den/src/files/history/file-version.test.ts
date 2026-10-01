import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import { describe, expect, it } from "vitest";
import type {
  SourceComparison,
  SourceFileVersion,
  SourceGitChange,
} from "../../api/types.ts";
import {
  fileVersionFromSnapshot,
  fileVersionAction,
  fileVersionActor,
  fileVersionBranchLabel,
  fileVersionFromComparison,
  fileVersionLandingLabel,
  fileVersionTitle,
  fileVersionViewTitle,
} from "./file-version.ts";
import { TEST_OWNER_PERSON_ID } from "../../platform/connection/host-identity-test.ts";

const checkout: SourceGitChange = {
  session_id: "", turn: 0, tool_call_id: "", tool_name: "",
  id: "t1",
  root_id: "r1",
  kind: "checkout",
  from_ref: "main",
  to_ref: "feature-x",
  ordinal: 4,
  observed_at: "2026-08-26T10:00:00Z",
};

function version(overrides: Partial<SourceFileVersion> = {}): SourceFileVersion {
  return {
    id: "v1",
    file_id: "file-a",
    workspace_kind: "project",
    root_id: "r1",
    path: "src/a.ts",
    state: "content",
    size_bytes: 4,
    capture_state: "stored",
    capture_quality: "exact",
    landing: "working_file",
    ordinal: 3,
    turn: 0,
    created_at: "2026-08-26T10:00:00Z",
    ...overrides,
  };
}

describe("fileVersionLandingLabel", () => {
  it("says nothing for a state the working file held", () => {
    expect(fileVersionLandingLabel(version())).toBeNull();
  });

  it("marks an AI edit the file on disk never received", () => {
    expect(fileVersionLandingLabel(version({ landing: "editor_document" })))
      .toBe("Never saved to disk");
  });
});

describe("fileVersionAction", () => {
  it("names each recorded effect", () => {
    expect(fileVersionAction("create")).toBe("Created");
    expect(fileVersionAction("write")).toBe("Edited");
    expect(fileVersionAction("rename")).toBe("Renamed");
    expect(fileVersionAction("delete")).toBe("Deleted");
  });

  it("names a retained state that no effect produced", () => {
    // The tracked baseline and preserved pre-images land here.
    expect(fileVersionAction(undefined)).toBe("Earlier state");
  });
});

describe("fileVersionActor", () => {
  it("names every contributor to a mixed publication", () => {
    expect(fileVersionActor(version({ origin: "user", contributors: [
      { origin: "agent", session_id: "chat", person_id: "", actor_label: "Fix auth", turn: 2, tool_call_id: "call", tool_name: "edit", worker_id: "" },
      { origin: "user", session_id: "chat", person_id: TEST_OWNER_PERSON_ID, actor_label: "", turn: 2, tool_call_id: "", tool_name: "", worker_id: "" },
    ] }))).toBe("From Fix auth · turn 2; You");
  });

  it("names a person other than the caller without claiming them as you", () => {
    expect(fileVersionActor(version({ origin: "user", contributors: [
      { origin: "user", session_id: "chat", person_id: TEST_OWNER_PERSON_ID, actor_label: "", turn: 2, tool_call_id: "", tool_name: "", worker_id: "" },
      { origin: "user", session_id: "chat", person_id: "00000000-0000-4000-8000-0000000000b2", actor_label: "", turn: 2, tool_call_id: "", tool_name: "", worker_id: "" },
    ] }))).toBe("You; Another person");
  });
  it("prefers an explicit actor label", () => {
    expect(fileVersionActor(version({ actor_label: "Outside app" })))
      .toBe("Outside app");
  });

  it("names the origin behind a recorded state", () => {
    expect(fileVersionActor(version({ origin: "agent" }))).toBe("AI");
    expect(fileVersionActor(version({ origin: "user" }))).toBe("A person");
    expect(fileVersionActor(version({ origin: "external" })))
      .toBe("External change");
  });

  it("claims no actor when no action was recorded", () => {
    expect(fileVersionActor(version())).toBe("");
  });

  it("names the command whose window observed a state", () => {
    const observed = version({
      origin: "external",
      cause: "command_window",
      command: {
        id: "w1",
        session_id: "s1",
        turn: 2,
        tool_name: "command",
        command_line: "cargo   build\n--release",
        state: "ended",
        ordinal: 3,
        started_at: "2026-08-26T10:00:00Z",
      },
    });
    expect(fileVersionActor(observed)).toBe("Command · cargo build --release");
  });

  it("names where a git movement went instead of an anonymous actor", () => {
    const caused = version({
      origin: "external",
      actor_label: "Outside app",
      git_change: checkout,
    });
    expect(fileVersionActor(caused)).toBe("main → feature-x");
    expect(
      fileVersionActor(
        version({
          origin: "external",
          git_change: { ...checkout, from_ref: undefined, to_ref: undefined },
        }),
      ),
    ).toBe("Git");
  });
});

describe("fileVersionTitle", () => {
  it("titles a git-caused version by its movement, not its op", () => {
    expect(fileVersionTitle(version({ op: "write", git_change: checkout })))
      .toBe("Git checkout");
    expect(fileVersionTitle(version({ op: "write" }))).toBe("Edited");
  });

  it("titles version and commit restores as restored", () => {
    expect(fileVersionTitle(version({ op: "write", cause: "version_restore" })))
      .toBe("Restored");
    expect(fileVersionTitle(version({ op: "write", cause: "git_commit_restore" })))
      .toBe("Restored");
  });

  it("matches the picker's title on a presented view", () => {
    const base = { versionId: "v1",
fileId: "file-a",
rootId: "r1",
path: "src/a.ts",
op: "write" as const,
ts: "t",
beforeAvailability: "available" as const,
sha256: null,
sizeBytes: 0,
availability: "available" as const, source: { kind: "text" as const, before: "", after: "" } };
    expect(fileVersionViewTitle({ ...base, gitChange: checkout }))
      .toBe("Git checkout");
    expect(fileVersionViewTitle(base)).toBe("Edited");
    expect(fileVersionViewTitle({ ...base, cause: "version_restore" }))
      .toBe("Restored");
  });
});

describe("fileVersionBranchLabel", () => {
  it("marks a worker-branch version", () => {
    expect(fileVersionBranchLabel(version({ workspace_kind: "worker" })))
      .toBe("Worker branch");
  });

  it("leaves a primary-workspace version unmarked", () => {
    expect(fileVersionBranchLabel(version())).toBeNull();
  });
});

describe("fileVersionFromComparison", () => {
  const selection = {
    versionId: "v1",
    fileId: "file-a",
    rootId: "r1",
    path: "src/a.ts",
    op: "write" as const,
    ts: "2026-08-26T10:00:00Z",
  };

  it("builds a view from both endpoints", () => {
    const diff: SourceComparison = {
      truncated: false,
      in_range: true,
      location_changed: false,
      before: {
        state: "content",
        size_bytes: 4,
        availability: "available",
        content: "was\n",
      },
      after: {
        state: "content",
        size_bytes: 3,
        availability: "available",
        content: "is\n",
        sha256: "abc",
        secret_screen: {
          truncated: false,
          screened_bytes: 3,
          spans: [],
        },
      },
    };
    const view = fileVersionFromComparison(selection, sourceReaderFixture().comparison(diff));
    expect(view?.source.kind).toBe("reader");
    expect(view?.sha256).toBe("abc");
    expect(view?.secretScreen?.truncated).toBe(false);
  });

  it("marks a snapshot with an absent file as a creation", () => {
    const created = fileVersionFromSnapshot({
      rootId: "r1", path: "new.ts", before: null, after: "made\n",
    });
    expect(created).toMatchObject({ op: "create", beforeAvailability: "absent", source: { kind: "text", before: "" } });
    expect(
      fileVersionFromSnapshot({ rootId: "r1", path: "old.ts", before: "was\n", after: "is\n" }),
    ).toMatchObject({ op: "write", beforeAvailability: "available", source: { kind: "text", before: "was\n" } });
  });

  it("distinguishes empty files from absent snapshot endpoints", () => {
    const snapshot = (before: string | null, after: string | null) =>
      fileVersionFromSnapshot({ rootId: "r1", path: "empty.txt", before, after });
    expect(snapshot("", "é")).toMatchObject({ op: "write", beforeAvailability: "available", availability: "available", sizeBytes: 2 });
    expect(snapshot(null, "")).toMatchObject({ op: "create", beforeAvailability: "absent", availability: "available" });
    expect(snapshot("", null)).toMatchObject({ op: "delete", beforeAvailability: "available", availability: "absent" });
    expect(snapshot("contents", "")).toMatchObject({ op: "write", availability: "available", sizeBytes: 0 });
  });

  it("refuses a comparison with no endpoints instead of showing a blank file", () => {
    expect(
      fileVersionFromComparison(selection, {
        in_range: false,
        location_changed: false,
      }),
    ).toBeNull();
  });
});
