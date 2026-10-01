import { stubClient } from "../../../test/client-fixture.ts";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { DetectionPack } from "../../../api/types.ts";
import { pickProjectFolder } from "../../../platform/files/folder.ts";
import { DETECTIONS_COPY } from "../../../settings/security/approvals-settings-copy.ts";
import { DetectionsPanel } from "./DetectionsPanel.tsx";

vi.mock("../../../platform/files/folder.ts", () => ({ pickProjectFolder: vi.fn() }));

const folderPicker = vi.mocked(pickProjectFolder);

function rule(
  overrides: Partial<DetectionPack["rules"][number]> & { id: string; title: string },
): DetectionPack["rules"][number] {
  return {
    level: "high",
    supported: true,
    ...overrides,
  };
}

function pack(overrides: Partial<DetectionPack> & { id: string; label: string }): DetectionPack {
  return {
    description: `${overrides.label} description`,
    source: "bundled",
    enabled: true,
    removable: false,
    rules: [rule({ id: `${overrides.id}-r1`, title: `${overrides.label} rule` })],
    ...overrides,
  };
}

const DETECTION_PACKS: DetectionPack[] = [
  pack({ id: "command-destructive", label: "Command destructive" }),
  pack({ id: "aws-cli", label: "AWS CLI" }),
  pack({ id: "azure-cli", label: "Azure CLI" }),
  pack({ id: "gcloud-cli", label: "Google Cloud CLI" }),
  pack({ id: "kubernetes-cli", label: "Kubernetes CLI" }),
  pack({
    id: "terraform-cli",
    label: "Terraform",
    equivalent_binaries: ["tofu", "terragrunt", "tf"],
  }),
  pack({ id: "publish-release", label: "Publish release" }),
  pack({ id: "external-services", label: "External services" }),
  pack({
    id: "gcp-structured-actions",
    label: "Google Cloud structured actions",
    source: "generated",
  }),
  pack({
    id: "azure-structured-actions",
    label: "Azure structured actions",
    source: "generated",
  }),
  pack({
    id: "egress-providers",
    label: "Observed connections",
    rules: [rule({ id: "egress-r1", title: "Metadata endpoint" })],
  }),
];

describe("DetectionsPanel", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    folderPicker.mockReset();
  });

  it("renders packs with labels, badges, and rule counts", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: DETECTION_PACKS, rejected: [] }),
    });

    render(() => <DetectionsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("detection-pack-aws-cli")).toBeTruthy();
    });

    for (const p of DETECTION_PACKS) {
      const row = screen.getByTestId(`detection-pack-${p.id}`);
      expect(row.textContent).toContain(p.label);
      expect(row.getAttribute("data-source")).toBe(p.source);
      expect(row.textContent).toContain(DETECTIONS_COPY.rulesLabel(p.rules.length));
    }

    expect(
      screen.getByTestId("detection-pack-gcp-structured-actions").textContent,
    ).toContain(DETECTIONS_COPY.generatedBadge);
    expect(
      screen.getByTestId("detection-pack-azure-structured-actions").textContent,
    ).toContain(DETECTIONS_COPY.generatedBadge);
    expect(
      screen.getByTestId("detection-pack-egress-providers-egress").textContent,
    ).toBe(DETECTIONS_COPY.egressBadge);
  });

  it("explains project overlay rows the merge refused", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({
        packs: DETECTION_PACKS,
        rejected: [
          { id: "typo-cli", code: "project_unknown_id", detail: "not installed" },
          { code: "invalid_entry", detail: "row is missing id" },
        ],
      }),
    });

    render(() => <DetectionsPanel client={client} />);

    await waitFor(() => screen.getByTestId("detection-rejected"));

    const unknown = screen.getByTestId("detection-rejected-row-0");
    expect(unknown.getAttribute("data-code")).toBe("project_unknown_id");
    expect(unknown.textContent).toContain("typo-cli");
    expect(unknown.textContent).toContain(
      DETECTIONS_COPY.rejectedReason("project_unknown_id"),
    );
    expect(unknown.textContent).toContain("not installed");

    // A row that never named a pack still has to be nameable in the list.
    const noID = screen.getByTestId("detection-rejected-row-1");
    expect(noID.textContent).toContain(DETECTIONS_COPY.rejectedRowLabel(""));
  });

  it("shows no rejected block when every overlay row applied", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: DETECTION_PACKS, rejected: [] }),
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-aws-cli"));
    expect(screen.queryByTestId("detection-rejected")).toBeNull();
  });

  it("toggles a pack and reflects the response", async () => {
    const updateDetectionPack = vi.fn().mockResolvedValue(
      pack({ id: "aws-cli", label: "AWS CLI", enabled: false }),
    );
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: DETECTION_PACKS, rejected: [] }),
      updateDetectionPack,
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-aws-cli-toggle"));

    const toggle = screen.getByTestId(
      "detection-pack-aws-cli-toggle",
    ) as HTMLInputElement;
    expect(toggle.checked).toBe(true);
    fireEvent.click(toggle);

    await waitFor(() => {
      expect(updateDetectionPack).toHaveBeenCalledWith("aws-cli", { enabled: false });
      expect(
        screen.getByTestId("detection-pack-aws-cli").getAttribute("data-enabled"),
      ).toBe("false");
    });
  });

  it("shows saveError and reverts on toggle failure", async () => {
    const updateDetectionPack = vi
      .fn()
      .mockRejectedValue(new Error("nope"));
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({
        rejected: [],
        packs: [pack({ id: "aws-cli", label: "AWS CLI", enabled: true })],
      }),
      updateDetectionPack,
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-aws-cli-toggle"));

    fireEvent.click(screen.getByTestId("detection-pack-aws-cli-toggle"));

    await waitFor(() => {
      expect(screen.getByTestId("detection-save-error").textContent).toBe(
        DETECTIONS_COPY.saveError,
      );
      expect(
        (screen.getByTestId("detection-pack-aws-cli-toggle") as HTMLInputElement)
          .checked,
      ).toBe(true);
    });
  });

  it("shows unsupported badge and reason", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({
        rejected: [],
        packs: [
          pack({
            id: "device-x",
            label: "Mine",
            source: "device",
            rules: [
              rule({
                id: "bad",
                title: "Base64 rule",
                supported: false,
                unsupported_reason: "field modifier base64 is not supported",
              }),
            ],
          }),
        ],
      }),
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-device-x"));

    fireEvent.click(
      screen.getByTestId("detection-pack-device-x").querySelector("summary")!,
    );

    expect(screen.getByTestId("detection-rule-unsupported").textContent).toBe(
      DETECTIONS_COPY.unsupportedBadge,
    );
    expect(screen.getByTestId("detection-pack-device-x").textContent).toContain(
      "field modifier base64 is not supported",
    );
  });

  it("renders alsoCovers when equivalents are present", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({
        rejected: [],
        packs: [
          pack({
            id: "terraform-cli",
            label: "Terraform",
            equivalent_binaries: ["tofu", "terragrunt"],
          }),
          pack({ id: "aws-cli", label: "AWS CLI", equivalent_binaries: [] }),
        ],
      }),
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-terraform-cli"));

    expect(
      screen.getByTestId("detection-pack-terraform-cli-also-covers").textContent,
    ).toBe(DETECTIONS_COPY.alsoCovers(["tofu", "terragrunt"]));
    expect(
      screen.queryByTestId("detection-pack-aws-cli-also-covers"),
    ).toBeNull();
  });

  it("shows empty and loadError states", async () => {
    const emptyClient = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: [], rejected: [] }),
    });

    const empty = render(() => <DetectionsPanel client={emptyClient} />);
    await waitFor(() => screen.getByTestId("detection-empty"));
    expect(screen.getByTestId("detection-empty").textContent).toBe(
      DETECTIONS_COPY.emptyState,
    );
    empty.unmount();

    const errClient = stubClient({
      listDetectionPacks: vi.fn().mockRejectedValue(new Error("boom")),
    });
    render(() => <DetectionsPanel client={errClient} />);
    await waitFor(() => screen.getByTestId("detection-load-error"));
    expect(screen.getByTestId("detection-load-error").textContent).toBe(
      DETECTIONS_COPY.loadError,
    );
  });

  it("reports a folder-picker failure without masquerading as a pack save", async () => {
    folderPicker.mockRejectedValueOnce(new Error("picker unavailable"));
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: [], rejected: [] }),
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-add-pack"));

    fireEvent.click(screen.getByTestId("detection-add-pack"));

    await waitFor(() => {
      expect(screen.getByTestId("detection-picker-error").textContent).toBe(
        DETECTIONS_COPY.pickerError,
      );
    });
    expect(screen.queryByTestId("detection-save-error")).toBeNull();
  });

  it("removes a device pack after confirm", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const deleteDetectionPack = vi.fn().mockResolvedValue(undefined);
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({
        rejected: [],
        packs: [
          pack({ id: "mine", label: "Mine", source: "device", removable: true }),
          pack({ id: "aws-cli", label: "AWS CLI", source: "bundled" }),
          pack({
            id: "gcp-structured-actions",
            label: "GCP generated",
            source: "generated",
          }),
          pack({
            id: "azure-structured-actions",
            label: "Azure generated",
            source: "generated",
          }),
        ],
      }),
      deleteDetectionPack,
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-mine-remove"));

    expect(screen.queryByTestId("detection-pack-aws-cli-remove")).toBeNull();
    for (const id of [
      "gcp-structured-actions",
      "azure-structured-actions",
    ]) {
      expect(screen.queryByTestId(`detection-pack-${id}-remove`)).toBeNull();
    }

    fireEvent.click(screen.getByTestId("detection-pack-mine-remove"));

    await waitFor(() => {
      expect(deleteDetectionPack).toHaveBeenCalledWith("mine");
      expect(screen.queryByTestId("detection-pack-mine")).toBeNull();
    });
  });

  it("attributes an extension's pack to its provider and offers no delete", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({
        rejected: [],
        packs: [
          pack({
            id: "acme-cloud",
            label: "Acme cloud",
            source: "extension",
            provider_pack_id: "acme/detections",
            load_warnings: ["rule rotate is not active: disabled in extension state"],
          }),
        ],
      }),
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-acme-cloud"));

    const row = screen.getByTestId("detection-pack-acme-cloud");
    expect(row.textContent).toContain(DETECTIONS_COPY.extensionBadge);
    expect(screen.getByTestId("detection-pack-acme-cloud-provider").textContent).toBe(
      DETECTIONS_COPY.providerHint("acme/detections"),
    );
    // Removing a provider also removes its packs.
    expect(screen.queryByTestId("detection-pack-acme-cloud-remove")).toBeNull();
    expect(row.getAttribute("data-tip")).toBe(
      DETECTIONS_COPY.removeExtensionHint("acme/detections"),
    );
    // Inactive rules remain visible with a warning.
    expect(screen.getByTestId("detection-pack-acme-cloud-warnings").textContent).toContain(
      "rule rotate is not active",
    );
  });

  it("renders egress note and badge with posture-aware copy", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: DETECTION_PACKS, rejected: [] }),
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-egress-providers-egress"));

    expect(screen.getByTestId("detection-egress-note").textContent).toBe(
      DETECTIONS_COPY.egressNote,
    );
    expect(screen.getByText(DETECTIONS_COPY.limits).textContent).toBe(
      DETECTIONS_COPY.limits,
    );
    expect(screen.getByText(DETECTIONS_COPY.levelNote).textContent).toContain(
      "Balanced",
    );
    expect(
      screen.getByTestId("detection-pack-egress-providers-egress").textContent,
    ).toBe("Holds connections");
  });

  it("sorts rules noisiest-first and hides zero recent-ask badges", async () => {
    const client = stubClient({
      listDetectionPacks: vi.fn().mockResolvedValue({
        rejected: [],
        packs: [
          pack({
            id: "aws-cli",
            label: "AWS CLI",
            rules: [
              rule({ id: "quiet", title: "Quiet rule" }),
              rule({ id: "noisy", title: "Noisy rule", recent_asks: 5 }),
              rule({ id: "medium", title: "Medium rule", recent_asks: 2 }),
            ],
          }),
        ],
      }),
    });

    render(() => <DetectionsPanel client={client} />);
    await waitFor(() => screen.getByTestId("detection-pack-aws-cli"));

    fireEvent.click(
      screen.getByTestId("detection-pack-aws-cli").querySelector("summary")!,
    );

    const titles = [...screen.getByTestId("detection-pack-aws-cli").querySelectorAll(".den-detections-rule__title")]
      .map((el) => el.textContent);
    expect(titles).toEqual(["Noisy rule", "Medium rule", "Quiet rule"]);
    expect(screen.getByTestId("detection-rule-noisy-recent-asks").textContent).toBe(
      DETECTIONS_COPY.recentAsksBadge(5),
    );
    expect(screen.queryByTestId("detection-rule-quiet-recent-asks")).toBeNull();
  });
});
