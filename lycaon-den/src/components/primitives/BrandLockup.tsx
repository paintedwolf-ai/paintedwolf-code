import { PRODUCT_NAME } from "../../../shared/brand.ts";

type Props = {
  class?: string;
};

export function BrandLockup(props: Props) {
  return (
    <span
      class="den-brand-lockup"
      classList={{ [props.class ?? ""]: Boolean(props.class) }}
    >
      <span class="den-brand-lockup__glyph" aria-hidden="true">
        <svg viewBox="0 0 64 64">
          <rect
            width="64"
            height="64"
            rx="14"
            fill="var(--den-brand-field)"
          />
          <g fill="var(--den-brand-glyph-mark)">
            <rect x="10" y="14" width="10" height="36" opacity=".25" />
            <rect x="22" y="14" width="10" height="36" opacity=".5" />
            <rect x="34" y="14" width="10" height="36" opacity=".75" />
            <rect x="46" y="14" width="10" height="36" />
          </g>
        </svg>
      </span>
      <span class="den-brand-lockup__label">{PRODUCT_NAME}</span>
    </span>
  );
}
