import { stubClient } from "../../../test/client-fixture.ts";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type {
  DetectionPack,
  DetectionPackImportResult,
} from "../../../api/types.ts";
import { LycaonApiError } from "../../../api/http.ts";
import { DETECTIONS_COPY } from "../../../settings/security/approvals-settings-copy.ts";
import { DetectionPackImportDialog } from "./DetectionPackImportDialog.tsx";
import * as folder from "../../../platform/files/folder.ts";
import { DetectionsPanel } from "./DetectionsPanel.tsx";

function samplePack(overrides: Partial<DetectionPack> = {}): DetectionPack {
  return {
    id: "scratch",
    label: "Scratch pack",
    description: "A test pack",
    source: "device",
    enabled: true,
    removable: true,
    rules: [
      {
        id: "ok",
        title: "Working rule",
        level: "high",
        supported: true,
      },
      {
        id: "bad",
        title: "Base64 rule",
        level: "high",
        supported: false,
        unsupported_reason: "field modifier base64 is not supported",
      },
    ],
    ...overrides,
  };
}

function dryRunResult(
  overrides: Partial<DetectionPackImportResult> = {},
): DetectionPackImportResult {
  return {
    dry_run: true,
    pack: samplePack(),
    rejected_rules: [{ file: "rules/broken.yml", reason: "invalid yaml" }],
    ignored: ["README.md", "link"],
    ...overrides,
  };
}

describe("DetectionPackImportDialog", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("posts dry-run and renders preview without committing", async () => {
    const importDetectionPack = vi.fn().mockResolvedValue(dryRunResult());
    const onImported = vi.fn();
    const client = stubClient({ importDetectionPack });

    render(() => (
      <DetectionPackImportDialog
        client={client}
        path="/tmp/scratch"
        onCancel={vi.fn()}
        onImported={onImported}
      />
    ));

    await waitFor(() => screen.getByTestId("detection-import-found"));

    expect(importDetectionPack).toHaveBeenCalledWith({
      path: "/tmp/scratch",
      dry_run: true,
      replace: undefined,
    });
    expect(screen.getByTestId("detection-import-found").textContent).toContain(
      "Scratch pack",
    );
    expect(screen.getByTestId("detection-import-rules").textContent).toContain(
      "Working rule",
    );
    expect(screen.getByTestId("detection-import-inactive").textContent).toContain(
      "Base64 rule",
    );
    expect(screen.getByTestId("detection-import-ignored").textContent).toContain(
      "README.md",
    );
    expect(onImported).not.toHaveBeenCalled();
    expect(
      importDetectionPack.mock.calls.every(
        (c: unknown[]) => (c[0] as { dry_run?: boolean }).dry_run !== false,
      ),
    ).toBe(true);
  });

  it("confirms with dry_run false and returns the pack", async () => {
    const pack = samplePack();
    const importDetectionPack = vi
      .fn()
      .mockResolvedValueOnce(dryRunResult())
      .mockResolvedValueOnce({
        dry_run: false,
        pack,
        rejected_rules: [],
        ignored: [],
      });
    const onImported = vi.fn();
    const client = stubClient({ importDetectionPack });

    render(() => (
      <DetectionPackImportDialog
        client={client}
        path="/tmp/scratch"
        onCancel={vi.fn()}
        onImported={onImported}
      />
    ));

    await waitFor(() => screen.getByTestId("detection-import-confirm"));
    fireEvent.click(screen.getByTestId("detection-import-confirm"));

    await waitFor(() => {
      expect(importDetectionPack).toHaveBeenLastCalledWith({
        path: "/tmp/scratch",
        dry_run: false,
        replace: undefined,
      });
      expect(onImported).toHaveBeenCalledWith(pack);
    });
  });

  it("enters replace mode on device collision", async () => {
    const importDetectionPack = vi
      .fn()
      .mockRejectedValueOnce(
        new LycaonApiError("exists", 409, "detection_pack_id_collision"),
      )
      .mockResolvedValueOnce(
        dryRunResult({ pack: samplePack({ id: "scratch" }) }),
      )
      .mockResolvedValueOnce({
        dry_run: false,
        pack: samplePack(),
        rejected_rules: [],
        ignored: [],
      });
    const onImported = vi.fn();
    const client = stubClient({ importDetectionPack });

    render(() => (
      <DetectionPackImportDialog
        client={client}
        path="/tmp/scratch"
        onCancel={vi.fn()}
        onImported={onImported}
      />
    ));

    await waitFor(() => screen.getByTestId("detection-import-replace-hint"));
    expect(screen.getByTestId("detection-import-replace-hint").textContent).toBe(
      DETECTIONS_COPY.addReplaceHint,
    );
    expect(screen.getByTestId("detection-import-confirm").textContent).toBe(
      DETECTIONS_COPY.addReplace,
    );
    expect(importDetectionPack).toHaveBeenNthCalledWith(2, {
      path: "/tmp/scratch",
      dry_run: true,
      replace: true,
    });

    fireEvent.click(screen.getByTestId("detection-import-confirm"));
    await waitFor(() => {
      expect(importDetectionPack).toHaveBeenLastCalledWith({
        path: "/tmp/scratch",
        dry_run: false,
        replace: true,
      });
      expect(onImported).toHaveBeenCalled();
    });
  });

  it("treats bundled collision as terminal", async () => {
    const importDetectionPack = vi
      .fn()
      .mockRejectedValueOnce(
        new LycaonApiError(
          "collides with a built-in pack",
          409,
          "detection_pack_id_collision",
        ),
      )
      .mockRejectedValueOnce(
        new LycaonApiError(
          "collides with a built-in pack",
          409,
          "detection_pack_id_collision",
        ),
      );
    const client = stubClient({ importDetectionPack });

    render(() => (
      <DetectionPackImportDialog
        client={client}
        path="/tmp/scratch"
        onCancel={vi.fn()}
        onImported={vi.fn()}
      />
    ));

    await waitFor(() => screen.getByTestId("detection-import-error"));
    expect(screen.getByTestId("detection-import-error").textContent).toContain(
      "built-in",
    );
    expect(screen.queryByTestId("detection-import-confirm")).toBeNull();
    expect(screen.getByTestId("detection-import-cancel")).toBeTruthy();
  });

  it("shows server message on 400 with only Cancel", async () => {
    const importDetectionPack = vi
      .fn()
      .mockRejectedValue(new LycaonApiError("no pack.yaml", 400, "invalid_request"));
    const client = stubClient({ importDetectionPack });

    render(() => (
      <DetectionPackImportDialog
        client={client}
        path="/tmp/empty"
        onCancel={vi.fn()}
        onImported={vi.fn()}
      />
    ));

    await waitFor(() => screen.getByTestId("detection-import-error"));
    expect(screen.getByTestId("detection-import-error").textContent).toContain(
      "no pack.yaml",
    );
    expect(screen.queryByTestId("detection-import-confirm")).toBeNull();
  });
});

describe("DetectionsPanel add-pack entry", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("cancelled picker makes no request and no dialog", async () => {
    vi.spyOn(folder, "pickProjectFolder").mockResolvedValue(null);
    const importDetectionPack = vi.fn();
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: [], rejected: [] }),
      importDetectionPack,
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-add-pack"));
    fireEvent.click(screen.getByTestId("detection-add-pack"));

    await waitFor(() => {
      expect(folder.pickProjectFolder).toHaveBeenCalled();
    });
    expect(importDetectionPack).not.toHaveBeenCalled();
    expect(screen.queryByTestId("detection-import-dialog")).toBeNull();
  });

  it("picker path opens dialog and dry-runs", async () => {
    vi.spyOn(folder, "pickProjectFolder").mockResolvedValue("/tmp/scratch");
    const importDetectionPack = vi.fn().mockResolvedValue(dryRunResult());
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: [], rejected: [] }),
      importDetectionPack,
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-add-pack"));
    fireEvent.click(screen.getByTestId("detection-add-pack"));

    await waitFor(() => screen.getByTestId("detection-import-dialog"));
    expect(importDetectionPack).toHaveBeenCalledWith({
      path: "/tmp/scratch",
      dry_run: true,
      replace: undefined,
    });
  });
});
