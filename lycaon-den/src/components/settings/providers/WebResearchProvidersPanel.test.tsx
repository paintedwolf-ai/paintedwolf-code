import { describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import type {
  WebResearchProviderMeta,
  WebResearchProvidersResponse,
} from "../../../api/types.ts";
import { WebResearchProvidersPanel } from "./WebResearchProvidersPanel.tsx";

function braveMeta(
  overrides: Partial<WebResearchProviderMeta> = {},
): WebResearchProviderMeta {
  return {
    id: "brave",
    kind: "keyed",
    label: "Brave Search",
    roles: ["results"],
    default_enabled: true,
    configured: false,
    credential_present: false,
    credential_source: "none",
    credential_slot: "brave-search",
    ...overrides,
  };
}

function statusWith(
  meta: WebResearchProviderMeta,
): WebResearchProvidersResponse {
  return {
    direct: {
      configured: true,
      card: { provider_id: "direct", kind: "direct", label: "Direct search" },
    },
    providers: [meta],
  };
}

describe("WebResearchProvidersPanel identity-replace remount", () => {
  it("keeps the open card mounted across a credential-save status replace", async () => {
    // Each save returns a new status object with the same provider id; the open card
    // stays keyed on the id and keeps its in-progress state.
    const updatedMeta = braveMeta({ credential_present: true, credential_source: "stored" });
    const replaceWebResearchCredential = vi.fn().mockResolvedValue(updatedMeta);
    const getWebResearchProviders = vi.fn().mockResolvedValue(statusWith(updatedMeta));

    function Harness() {
      const [status, setStatus] = createSignal(statusWith(braveMeta()));
      return (
        <WebResearchProvidersPanel
          client={{ replaceWebResearchCredential, getWebResearchProviders } as never}
          status={status()}
          enabledIds={new Set(["brave"])}
          domainGuess={false}
          onDomainGuessChange={() => undefined}
          onStatus={setStatus}
          onAddProvider={() => undefined}
          onRemoveProvider={() => undefined}
        />
      );
    }

    const { getByTestId } = render(() => <Harness />);

    await waitFor(() => {
      expect(getByTestId("web-research-row-brave")).toBeTruthy();
    });
    fireEvent.click(getByTestId("web-research-row-brave"));
    await waitFor(() => {
      expect(getByTestId("web-research-card-brave")).toBeTruthy();
    });
    const cardBefore = getByTestId("web-research-card-brave");
    const keyInput = getByTestId("web-research-key-brave") as HTMLInputElement;
    fireEvent.input(keyInput, { target: { value: "brave-key-123" } });

    fireEvent.click(getByTestId("web-research-save-key-brave"));
    await waitFor(() => {
      expect(replaceWebResearchCredential).toHaveBeenCalledWith(
        "brave",
        "brave-key-123",
      );
    });
    await waitFor(() => {
      expect(getByTestId("web-research-key-status-brave").textContent).toBe(
        "Key saved",
      );
    });

    expect(getByTestId("web-research-card-brave")).toBe(cardBefore);
  });

  it("remounts the detail when navigating back and selecting the direct card", async () => {
    function Harness() {
      const [status, setStatus] = createSignal(statusWith(braveMeta()));
      return (
        <WebResearchProvidersPanel
          client={{} as never}
          status={status()}
          enabledIds={new Set(["brave", "direct"])}
          domainGuess={false}
          onDomainGuessChange={() => undefined}
          onStatus={setStatus}
          onAddProvider={() => undefined}
          onRemoveProvider={() => undefined}
        />
      );
    }

    const { getByTestId } = render(() => <Harness />);

    await waitFor(() => getByTestId("web-research-row-brave"));
    fireEvent.click(getByTestId("web-research-row-brave"));
    await waitFor(() => getByTestId("web-research-card-brave"));

    fireEvent.click(getByTestId("web-research-back"));
    await waitFor(() => getByTestId("web-research-row-direct"));
    fireEvent.click(getByTestId("web-research-row-direct"));
    await waitFor(() => {
      expect(getByTestId("web-research-direct-card")).toBeTruthy();
    });
  });
});
