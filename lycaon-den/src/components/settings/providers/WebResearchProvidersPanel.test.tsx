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

const REJECTION = {
  code: "SUBSCRIPTION_TOKEN_INVALID",
  observed_at: "2026-11-23T00:00:00Z",
};

function stackexchangeMeta(
  overrides: Partial<WebResearchProviderMeta> = {},
): WebResearchProviderMeta {
  return {
    id: "stackexchange",
    kind: "keyless",
    label: "Stack Overflow",
    roles: ["results"],
    default_enabled: true,
    configured: true,
    credential_present: true,
    credential_source: "stored",
    credential_slot: "stackexchange",
    ...overrides,
  };
}

function renderPanel(
  initial: WebResearchProvidersResponse,
  client: Record<string, unknown>,
  enabled: string[],
) {
  function Harness() {
    const [status, setStatus] = createSignal(initial);
    return (
      <WebResearchProvidersPanel
        client={client as never}
        status={status()}
        enabledIds={new Set(enabled)}
        domainGuess={false}
        onDomainGuessChange={() => undefined}
        onStatus={setStatus}
        onAddProvider={() => undefined}
        onRemoveProvider={() => undefined}
      />
    );
  }
  return render(() => <Harness />);
}

describe("WebResearchProvidersPanel rejected credentials", () => {
  it("marks a standalone provider row, card and key field as rejected", async () => {
    const meta = braveMeta({
      configured: true,
      credential_present: true,
      credential_source: "stored",
      credential_rejected: REJECTION,
    });
    const { getByTestId } = renderPanel(statusWith(meta), {}, ["brave"]);

    const row = await waitFor(() => getByTestId("web-research-row-brave"));
    const rowStatus = row.querySelector(".den-settings-provider-status");
    expect(rowStatus?.getAttribute("data-configured")).toBe("false");
    expect(rowStatus?.textContent).toBe("Key rejected");

    fireEvent.click(row);
    const card = await waitFor(() => getByTestId("web-research-card-brave"));
    const cardStatus = card.querySelector("header .den-settings-provider-status")
      ?? card.querySelector(".den-settings-provider-status");
    expect(cardStatus?.getAttribute("data-configured")).toBe("false");

    const keyStatus = getByTestId("web-research-key-status-brave");
    expect(keyStatus.textContent).toBe("Key rejected");
    expect(keyStatus.getAttribute("data-configured")).toBe("false");
    expect(keyStatus.classList.contains("den-settings-warn")).toBe(true);
    expect(getByTestId("web-research-clear-key-brave")).toBeTruthy();
  });

  it("marks a Direct optional key as rejected", async () => {
    const status: WebResearchProvidersResponse = {
      direct: {
        configured: true,
        card: { provider_id: "direct", kind: "direct", label: "Direct search" },
      },
      providers: [stackexchangeMeta({ credential_rejected: REJECTION })],
    };
    const { getByTestId } = renderPanel(status, {}, ["direct"]);

    fireEvent.click(await waitFor(() => getByTestId("web-research-row-direct")));
    await waitFor(() => getByTestId("web-research-direct-credential-stackexchange"));
    const keyStatus = getByTestId("web-research-key-status-stackexchange");
    expect(keyStatus.textContent).toBe("Key rejected");
    expect(keyStatus.getAttribute("data-configured")).toBe("false");
    expect(keyStatus.classList.contains("den-settings-warn")).toBe(true);
  });

  it("reloads provider state after a provider test", async () => {
    const rejected = braveMeta({
      configured: true,
      credential_present: true,
      credential_source: "stored",
      credential_rejected: REJECTION,
    });
    const accepted = braveMeta({
      configured: true,
      credential_present: true,
      credential_source: "stored",
    });
    const testWebResearchProvider = vi.fn().mockResolvedValue({ ok: true, latency_ms: 12 });
    const getWebResearchProviders = vi.fn().mockResolvedValue(statusWith(accepted));
    const { getByTestId } = renderPanel(
      statusWith(rejected),
      { testWebResearchProvider, getWebResearchProviders },
      ["brave"],
    );

    fireEvent.click(await waitFor(() => getByTestId("web-research-row-brave")));
    await waitFor(() => getByTestId("web-research-card-brave"));
    expect(getByTestId("web-research-key-status-brave").textContent).toBe("Key rejected");

    fireEvent.click(getByTestId("web-research-test-brave"));
    await waitFor(() => {
      expect(getByTestId("web-research-key-status-brave").textContent).toBe("Key saved");
    });
    expect(testWebResearchProvider).toHaveBeenCalledWith("brave");
    expect(getWebResearchProviders).toHaveBeenCalledTimes(1);
    expect(getByTestId("web-research-key-status-brave").getAttribute("data-configured")).toBe("true");
  });

  it("reloads provider state after a Direct test", async () => {
    const withRejection: WebResearchProvidersResponse = {
      direct: {
        configured: true,
        card: { provider_id: "direct", kind: "direct", label: "Direct search" },
      },
      providers: [stackexchangeMeta({ credential_rejected: REJECTION })],
    };
    const cleared = { ...withRejection, providers: [stackexchangeMeta()] };
    const testWebResearchProvider = vi.fn().mockResolvedValue({ ok: false, message: "no results" });
    const getWebResearchProviders = vi.fn().mockResolvedValue(cleared);
    const { getByTestId } = renderPanel(
      withRejection,
      { testWebResearchProvider, getWebResearchProviders },
      ["direct"],
    );

    fireEvent.click(await waitFor(() => getByTestId("web-research-row-direct")));
    await waitFor(() => getByTestId("web-research-direct-credential-stackexchange"));
    fireEvent.click(getByTestId("web-research-direct-test"));
    await waitFor(() => {
      expect(getByTestId("web-research-key-status-stackexchange").textContent).toBe("Key saved");
    });
    expect(testWebResearchProvider).toHaveBeenCalledWith("direct");
    expect(getByTestId("web-research-direct-test-result").textContent).toContain("no results");
  });
});
