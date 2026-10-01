import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { WhatsNewDialog } from "./WhatsNewDialog.tsx";
import { REPOSITORY_URL } from "../../../shared/brand.ts";

const { writeClipboardText, confirmAndOpenExternalLink } = vi.hoisted(() => ({
  writeClipboardText: vi.fn(async (_text: string) => undefined),
  confirmAndOpenExternalLink: vi.fn(async (_href: string) => true),
}));

vi.mock("../../utils/clipboard.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../utils/clipboard.ts")>()),
  writeClipboardText,
}));

vi.mock("../../platform/desktop/external-link.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/desktop/external-link.ts")>()),
  confirmAndOpenExternalLink,
}));

describe("WhatsNewDialog", () => {
  it("renders the full release notes and lets the reader acknowledge them", () => {
    const onClose = vi.fn();
    const onGotIt = vi.fn();
    render(() => (
      <WhatsNewDialog
        open
        version="0.2.0"
        notes="### Changed\n\n- Prepared the next pre-1.0 release."
        onClose={onClose}
        onGotIt={onGotIt}
      />
    ));

    expect(screen.getByTestId("whats-new-dialog").textContent).toMatch(
      /What’s new in 0\.2\.0/,
    );
    expect(screen.getByTestId("whats-new-dialog-notes").textContent).toMatch(
      /Prepared the next pre-1\.0 release/,
    );

    fireEvent.click(screen.getByTestId("whats-new-dialog-got-it"));
    expect(onGotIt).toHaveBeenCalledTimes(1);
  });

  it("closes from the close control, backdrop, or Escape without acknowledging", () => {
    const onClose = vi.fn();
    const onGotIt = vi.fn();
    render(() => (
      <WhatsNewDialog
        open
        version="0.2.0"
        notes="### Changed"
        onClose={onClose}
        onGotIt={onGotIt}
      />
    ));

    fireEvent.click(screen.getByTestId("whats-new-dialog-close"));
    fireEvent.click(screen.getByTestId("whats-new-dialog-backdrop"));
    fireEvent.keyDown(screen.getByTestId("whats-new-dialog"), { key: "Escape" });

    expect(onClose).toHaveBeenCalledTimes(3);
    expect(onGotIt).not.toHaveBeenCalled();
  });

  it("keeps every close path disabled while acknowledging the release", () => {
    const onClose = vi.fn();
    render(() => (
      <WhatsNewDialog
        open
        busy
        version="0.2.0"
        notes="### Changed"
        onClose={onClose}
        onGotIt={() => undefined}
      />
    ));

    fireEvent.click(screen.getByTestId("whats-new-dialog-close"));
    fireEvent.click(screen.getByTestId("whats-new-dialog-backdrop"));
    fireEvent.keyDown(screen.getByTestId("whats-new-dialog"), { key: "Escape" });

    expect(onClose).not.toHaveBeenCalled();
  });

  it("copies the website page for this release", async () => {
    render(() => (
      <WhatsNewDialog
        open
        version="v1.2.0"
        notes="### Added"
        onClose={() => undefined}
        onGotIt={() => undefined}
      />
    ));

    fireEvent.click(screen.getByTestId("whats-new-dialog-copy-link"));
    expect(writeClipboardText).toHaveBeenCalledWith("https://paintedwolf.ai/releases/1.2.0/");
    await waitFor(() => {
      expect(screen.getByTestId("whats-new-dialog-copy-link").textContent).toBe("Link copied");
    });

    fireEvent.click(screen.getByTestId("whats-new-dialog-star"));
    expect(confirmAndOpenExternalLink).toHaveBeenCalledWith(REPOSITORY_URL);
  });
});
