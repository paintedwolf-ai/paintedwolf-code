// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { ErrorBoundary } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { MarkSecretFailure } from "./mark-secret-failure.ts";
import { SECRET_SPAN_COPY } from "./secret-span-copy.ts";
import { MarkSecretDialog, type MarkSecretTarget } from "./MarkSecretDialog.tsx";

function target(overrides: Partial<MarkSecretTarget> = {}): MarkSecretTarget {
  return {
    path: ".env",
    line: 22,
    dirty: false,
    preview: vi.fn(async () => ({
      eligible: true,
      start: 0,
      end: 36,
      rune_length: 36,
      byte_length: 36,
      shape: "a1b2c3a1-a1b2-1231-a1b2-a1b2c3a1b2c3 (36 characters)",
      trimmed_leading: 1,
      trimmed_trailing: 1,
    })),
    mark: vi.fn(async () => "Stripe test key"),
    ...overrides,
  };
}

function open(overrides: Partial<MarkSecretTarget> = {}) {
  // The dialog portals to document.body, so queries go through `screen`.
  return render(() => (
    <MarkSecretDialog
      target={target(overrides)}
      onClose={vi.fn()}
      onMarked={vi.fn()}
      onError={vi.fn()}
    />
  ));
}

describe("MarkSecretDialog ineligible preview", () => {
  it("says a managed secret already protects the selection", async () => {
    open({
      preview: vi.fn(async () => ({
        eligible: false,
        reason: "already_protected" as const,
        start: 0,
        end: 24,
        rune_length: 24,
        byte_length: 24,
      })),
    });
    const refusal = await screen.findByRole("alert");
    expect(refusal.textContent).toBe(SECRET_SPAN_COPY.sheetAlreadyProtected);
  });
});

/** Revision conflicts appear in the dialog without replacing the file stage. */
describe("MarkSecretDialog refused preview", () => {
  it("reports the refusal in the sheet instead of reaching the stage boundary", async () => {
    const stageFailed = vi.fn();
    render(() => (
      <ErrorBoundary
        fallback={(err) => {
          stageFailed(err);
          return <p data-testid="stage-boundary">Reload view</p>;
        }}
      >
        <MarkSecretDialog
          target={target({
            preview: vi.fn(async () => {
              throw new MarkSecretFailure("That file changed while the sheet was open.");
            }),
          })}
          onClose={vi.fn()}
          onMarked={vi.fn()}
          onError={vi.fn()}
        />
      </ErrorBoundary>
    ));

    const refusal = await screen.findByTestId("mark-secret-preview-error");
    expect(refusal.textContent).toContain("That file changed");
    expect(stageFailed).not.toHaveBeenCalled();
    expect(screen.queryByTestId("stage-boundary")).toBeNull();
    expect((screen.getByTestId("mark-secret-submit") as HTMLButtonElement).disabled).toBe(
      true,
    );
  });
});

describe("MarkSecretDialog layout", () => {
  it("puts every field in a scrollable body between the header and the footer", () => {
    open();
    const dialog = screen.getByTestId("mark-secret-dialog");
    const body = dialog.querySelector(".den-dialog__body");
    expect(body).toBeTruthy();

    for (const id of ["mark-secret-receipt", "mark-secret-name", "mark-secret-purpose"]) {
      expect(body!.contains(screen.getByTestId(id))).toBe(true);
    }
    const footer = dialog.querySelector(".den-dialog__footer");
    expect(footer).toBeTruthy();
    expect(body!.contains(screen.getByTestId("mark-secret-submit"))).toBe(false);
    expect(body!.contains(screen.getByTestId("mark-secret-origin"))).toBe(false);
  });

  it("frames that body as a scrollport so a cramped window scrolls", () => {
    open();
    const body = screen.getByTestId("mark-secret-dialog").querySelector(".den-dialog__body");
    expect(body?.getAttribute("data-den-scrollport")).toBe("y");
    expect(body?.querySelector(":scope > .den-scrollport__viewport")).toBeTruthy();
  });

  it("states the captured length once", async () => {
    open();
    const receipt = screen.getByTestId("mark-secret-receipt");
    await waitFor(() => expect(receipt.textContent).toContain("36 characters"));
    const occurrences = receipt.textContent?.match(/36 characters/g) ?? [];
    expect(occurrences).toHaveLength(1);
  });

  it("requires a name and purpose", async () => {
    open();
    const submit = screen.getByTestId("mark-secret-submit");
    await waitFor(() =>
      expect(screen.getByTestId("mark-secret-receipt").textContent).toContain("36 characters"),
    );
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    fireEvent.input(screen.getByTestId("mark-secret-name"), { target: { value: "Registry key" } });
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    fireEvent.input(screen.getByTestId("mark-secret-purpose"), { target: { value: "Publish packages" } });
    expect((submit as HTMLButtonElement).disabled).toBe(false);
  });
});
