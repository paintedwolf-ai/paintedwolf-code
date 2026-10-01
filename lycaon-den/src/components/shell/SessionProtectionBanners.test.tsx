import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import {
  MEDIATION_UNAVAILABLE_COPY,
  SANDBOX_BYPASS_COPY,
  SessionProtectionBanners,
  sessionProtectionPresent,
} from "./SessionProtectionBanners.tsx";

describe("SessionProtectionBanners", () => {
  it("keeps mediation unavailable and sandbox bypass as distinct banners", () => {
    const { getByTestId } = render(() => (
      <SessionProtectionBanners
        protection={{
          mediation_unavailable: true,
          sandbox_bypass: true,
        }}
      />
    ));
    expect(getByTestId("protection-mediation-unavailable").textContent).toContain(
      MEDIATION_UNAVAILABLE_COPY,
    );
    expect(getByTestId("protection-sandbox-bypass").textContent).toContain(
      SANDBOX_BYPASS_COPY,
    );
  });

  it("shows partial startup overrides without claiming full bypass", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <SessionProtectionBanners
        protection={{
          control_plane_reads_allowed: true,
          additional_write_roots: ["/shared/build-cache"],
        }}
      />
    ));
    const banner = getByTestId("protection-boundary-overrides");
    expect(banner.textContent).toContain("app configuration");
    expect(banner.textContent).toContain("/shared/build-cache");
    expect(queryByTestId("protection-sandbox-bypass")).toBeNull();
    expect(sessionProtectionPresent({ control_plane_reads_allowed: true })).toBe(
      true,
    );
    expect(
      sessionProtectionPresent({ additional_write_roots: ["/shared/build-cache"] }),
    ).toBe(true);
  });

  it("uses the full bypass banner when it subsumes partial overrides", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <SessionProtectionBanners
        protection={{ sandbox_bypass: true, control_plane_reads_allowed: true }}
      />
    ));
    expect(getByTestId("protection-sandbox-bypass")).toBeTruthy();
    expect(queryByTestId("protection-boundary-overrides")).toBeNull();
  });

  it("allows dismissing protection banners", () => {
    const onDismiss = vi.fn();
    const { getByTestId, queryByTestId } = render(() => (
      <SessionProtectionBanners
        protection={{ mediation_unavailable: true }}
        onDismiss={onDismiss}
      />
    ));
    const banner = getByTestId("protection-mediation-unavailable");
    const closeBtn = banner.querySelector("[data-testid='system-nudge-dismiss']");
    expect(closeBtn).toBeTruthy();
    fireEvent.click(closeBtn!);
    expect(onDismiss).toHaveBeenCalledWith("mediation_unavailable");
    expect(queryByTestId("protection-mediation-unavailable")).toBeNull();
  });

  it("sessionProtectionPresent is false when empty or dismissed", () => {
    expect(sessionProtectionPresent(undefined)).toBe(false);
    expect(sessionProtectionPresent({})).toBe(false);
    expect(
      sessionProtectionPresent({ mediation_unavailable: true }),
    ).toBe(true);
    expect(
      sessionProtectionPresent(
        { mediation_unavailable: true },
        { mediation_unavailable: true },
      ),
    ).toBe(false);
  });
});
