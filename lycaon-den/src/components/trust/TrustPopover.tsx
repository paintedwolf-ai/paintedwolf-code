import { For, Show } from "solid-js";
import type { ProjectTrust } from "../../api/types.ts";
import { TRUST_COPY } from "../../settings/security/trust-copy.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";

const MAX_POPOVER_ROWS = 4;

export function TrustPopoverBody(props: { trust: ProjectTrust; onOpen?: () => void; opening?: boolean }) {
  const surfaces = () => props.trust.surfaces
    .map(surface => ({ ...surface, changes: props.trust.review.changes.filter(change => change.surface_ids.includes(surface.id)).length }))
    .filter(surface => surface.changes > 0);
  const rows = () => surfaces().slice(0, MAX_POPOVER_ROWS);
  return <>
    <p class="den-status-popover__title" data-testid="trust-popover-title" {...chromeProps()}>
      {TRUST_COPY.reviewCount(props.trust.review.changes.length)}
    </p>
    <p class="den-status-popover__hint">{TRUST_COPY.reviewStatus(props.trust.unread_count)}</p>
    <Show when={rows().length > 0}>
      <ul class="trust-popover__list"><For each={rows()}>{surface =>
        <li>
          <button type="button" class="trust-popover__item" data-testid={`trust-popover-row-${surface.id}`} disabled={props.opening} onClick={() => props.onOpen?.()}
            aria-label={`${surface.label}. ${TRUST_COPY.reviewCount(surface.changes)}. Open trust changes`}>
            <span class="trust-popover__item-dot" data-state={props.trust.unread_count > 0 ? "changed" : "on"} aria-hidden="true" />
            <span class="trust-popover__item-label">{surface.label}</span>
            <span class="trust-popover__item-value">{surface.changes} {surface.changes === 1 ? "file" : "files"}</span>
          </button>
        </li>
      }</For></ul>
    </Show>
  </>;
}
