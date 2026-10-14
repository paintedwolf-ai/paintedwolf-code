import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { Announcer, announce } from "./announcer.tsx";

describe("Announcer", () => {
  it("is one polite atomic status region that speaks each announcement", async () => {
    const { getByTestId } = render(() => <Announcer />);
    const region = getByTestId("app-announcer");
    expect(region.getAttribute("role")).toBe("status");
    expect(region.getAttribute("aria-live")).toBe("polite");
    expect(region.getAttribute("aria-atomic")).toBe("true");

    announce("  ");
    await Promise.resolve();
    expect(region.textContent).toBe("");

    announce("Needs approval: write");
    expect(region.textContent).toBe("");
    await Promise.resolve();
    expect(region.textContent).toBe("Needs approval: write");

    announce("First");
    announce("Second");
    await Promise.resolve();
    expect(region.textContent).toBe("Second");
  });
});
