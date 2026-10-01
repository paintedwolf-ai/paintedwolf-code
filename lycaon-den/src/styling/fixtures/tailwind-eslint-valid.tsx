import { cn } from "../../shared/cn.ts";

/** Fixture for eslint-tailwind.contract.test.ts — not imported by the app. */
export function TailwindEslintValidFixture() {
  return (
    <div
      class={cn("flex flex-col gap-2 bg-den-bg text-den-text", "rounded-den")}
      data-testid="tailwind-eslint-valid-fixture"
    />
  );
}
