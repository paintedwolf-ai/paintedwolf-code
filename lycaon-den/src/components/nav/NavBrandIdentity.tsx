import { PRODUCT_NAME } from "../../../shared/brand.ts";
import { BrandLockup } from "../primitives/BrandLockup.tsx";

type Props = {
  onClick: () => void;
};

/** Persistent product identity at the top of the left nav rail. */
export function NavBrandIdentity(props: Props) {
  return (
    <button
      type="button"
      class="den-nav-brand"
      data-testid="nav-brand"
      aria-label={PRODUCT_NAME}
      onClick={() => props.onClick()}
    >
      <BrandLockup />
    </button>
  );
}
