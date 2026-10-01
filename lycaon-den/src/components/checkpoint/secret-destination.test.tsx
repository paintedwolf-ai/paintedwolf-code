import { describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import type { ApprovalSecretDestinationKind } from "../../api/types.ts";
import {
  approvalOptionFixture,
  toolApprovalFixture,
} from "../../chat/checkpoint/approval-test-fixtures.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";

// Builds a secret approval checkpoint fixture for destination kind tests.
function secretCard(
  destination: string,
  destinationKind: ApprovalSecretDestinationKind,
): PendingCheckpoint {
  return {
    checkpointId: "secret-destination-1",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-09-07T00:00:00Z",
    tool_approval: toolApprovalFixture({
      tool: "http_request",
      stage: "pre_send",
      subject: {
        kind: "secret",
        title: "A protected value will be used in this HTTP request",
        targets: [{ kind: "secret", label: "A managed secret" }],
      },
      presentation: {
        action: "Send HTTP request",
        gate: "secret_outbound",
        impact: "This HTTP request would send the value.",
        location: {
          origin: "in request",
          destination,
          origin_kind: "field",
          destination_kind: destinationKind,
        },
      },
      recommendedOptionId: "send_unchanged",
      options: [
        approvalOptionFixture({ id: "send_unchanged", rung: "unchanged", title: "Send" }),
      ],
    }),
  };
}

function mount(checkpoint: PendingCheckpoint) {
  return render(() => (
    <ApprovalCard
      checkpoint={checkpoint}
      resolving={false}
      onToolApproval={() => {}}
      onContentApply={() => {}}
    />
  ));
}

describe("secret card destination", () => {
  it("says the model reads it when the destination is the model provider", () => {
    const view = mount(secretCard("Fireworks 1", "model_provider"));
    expect(view.getByTestId("approval-location-destination").textContent).toBe("Fireworks 1");
    const chip = view.getByTestId("approval-location-destination-kind");
    expect(chip.textContent).toBe("your model provider — the model reads this");
    expect(chip.getAttribute("data-destination-kind")).toBe("model_provider");
  });

  it("marks a service without claiming the model sees it", () => {
    const view = mount(secretCard("http://localhost:3000", "service"));
    const chip = view.getByTestId("approval-location-destination-kind");
    expect(chip.textContent).toBe("service");
    expect(chip.textContent).not.toContain("model");
  });

  it("says a command's onward destinations are not observed", () => {
    const view = mount(secretCard("this command's own process", "process"));
    expect(view.getByTestId("approval-location-destination-kind").textContent).toBe(
      "destinations not observed",
    );
  });

  it("marks a file without claiming the model sees it", () => {
    const view = mount(secretCard("Local file: .env", "file"));
    const chip = view.getByTestId("approval-location-destination-kind");
    expect(chip.textContent).toBe("file");
    expect(chip.textContent).not.toContain("model");
  });

  it("names why an image was not screened and keeps the redacted send disabled", () => {
    const reason = "Image text not screened: text recognition is unavailable on this device";
    const note =
      "Redaction is not offered here: the host could not read the text in this image, so it cannot find or mask a credential in it.";
    const checkpoint = secretCard("model provider", "model_provider");
    checkpoint.tool_approval = toolApprovalFixture({
      tool: "visual_perception",
      stage: "pre_send",
      subject: {
        kind: "secret",
        title: "Image text could not be screened before visual perception",
        targets: [{ kind: "secret", label: reason }],
      },
      presentation: {
        action: "Send visual perception",
        gate: "secret_outbound",
        impact: "The model at model provider would read this image, including any text the host could not check in this request.",
        option_note: note,
        location: {
          origin: "in an image",
          destination: "model provider",
          origin_kind: "field",
          destination_kind: "model_provider",
        },
      },
      recommendedOptionId: "release_task",
      options: [
        approvalOptionFixture({
          id: "send_redacted",
          kind: "redacted",
          rung: "redacted",
          title: "Send redacted",
          decision_action: "redact",
          disabled: true,
          note,
        }),
        approvalOptionFixture({ id: "send_unchanged", rung: "unchanged", title: "Send unchanged" }),
        approvalOptionFixture({
          id: "release_task",
          kind: "lease",
          rung: "chat",
          scope: "chat",
          title: "Send unchanged for this chat",
        }),
      ],
    });
    const view = mount(checkpoint);
    expect(view.container.textContent).toContain(reason);
    expect(view.getByTestId("approval-option-note").textContent).toContain(note);
  });
});
