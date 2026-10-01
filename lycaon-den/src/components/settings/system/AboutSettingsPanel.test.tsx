import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { AboutSettingsPanel } from "./AboutSettingsPanel.tsx";
import { REPOSITORY_URL, WEBSITE_URL } from "../../../../shared/brand.ts";

const mocks = vi.hoisted(() => ({
  writeClipboardText: vi.fn(async (_text: string) => undefined),
  confirmAndOpenExternalLink: vi.fn(async (_href: string) => true),
}));

vi.mock("../../../utils/clipboard.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../utils/clipboard.ts")>()),
  writeClipboardText: mocks.writeClipboardText,
}));

vi.mock("../../../platform/desktop/external-link.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../platform/desktop/external-link.ts")>()),
  confirmAndOpenExternalLink: mocks.confirmAndOpenExternalLink,
}));

describe("AboutSettingsPanel", () => {
  it("copies the website link and confirms the copy", async () => {
    const { getByTestId } = render(() => (
      <AboutSettingsPanel version="0.1.0" />
    ));

    fireEvent.click(getByTestId("about-copy-link"));
    expect(mocks.writeClipboardText).toHaveBeenCalledWith(WEBSITE_URL);
    await waitFor(() => {
      expect(getByTestId("about-copy-link").textContent).toBe("Link copied");
    });
  });

  it("opens the repository and website through the external-link confirmation", () => {
    const { getByTestId } = render(() => (
      <AboutSettingsPanel version="0.1.0" />
    ));

    fireEvent.click(getByTestId("about-star"));
    fireEvent.click(getByTestId("about-website"));
    expect(mocks.confirmAndOpenExternalLink.mock.calls.map((c) => c[0])).toEqual([
      REPOSITORY_URL,
      WEBSITE_URL,
    ]);
  });

  it("shows the running version", () => {
    const { getByTestId } = render(() => (
      <AboutSettingsPanel version="0.1.0" />
    ));

    expect(getByTestId("about-version").textContent).toContain("0.1.0");
  });

  it("states the no-telemetry posture", () => {
    const { getByTestId } = render(() => (
      <AboutSettingsPanel version="0.1.0" />
    ));

    expect(getByTestId("about-privacy").textContent).toMatch(/no telemetry/i);
  });

  it("offers a single Report a bug… entry that opens the dialog", async () => {
    const { getByTestId } = render(() => (
      <AboutSettingsPanel version="0.1.0" />
    ));

    expect(getByTestId("about-report-bug").textContent).toMatch(
      /Report a bug/i,
    );
    fireEvent.click(getByTestId("about-report-bug"));
    await waitFor(() => {
      expect(screen.getByTestId("report-bug-dialog")).toBeTruthy();
    });
  });

  it("offers a Third-party software row", () => {
    const { getByTestId } = render(() => (
      <AboutSettingsPanel version="0.1.0" />
    ));

    expect(getByTestId("about-third-party").textContent).toMatch(
      /Third-party software/i,
    );
  });
});
