import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { PRODUCT_NAME } from "../../../shared/brand.ts";
import { NavBrandIdentity } from "./NavBrandIdentity.tsx";

describe("NavBrandIdentity", () => {
  it("shows the product name and calls onClick", () => {
    const onClick = vi.fn();
    render(() => <NavBrandIdentity onClick={onClick} />);
    expect(screen.getByText(PRODUCT_NAME)).toBeTruthy();
    expect(screen.queryByTestId("nav-brand-notice-count")).toBeNull();
    fireEvent.click(screen.getByTestId("nav-brand"));
    expect(onClick).toHaveBeenCalledOnce();
  });
});
