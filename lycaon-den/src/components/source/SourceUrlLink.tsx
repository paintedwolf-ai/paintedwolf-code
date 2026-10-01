import { Show } from "solid-js";
import {
  confirmAndOpenExternalLink,
  isExternalLinkHref,
} from "../../platform/desktop/external-link.ts";
import { clickSelectedText } from "../../platform/interaction/selection-gesture.ts";
import { cn } from "../../shared/cn.ts";

export type SourceUrlLinkProps = {
  url: string;
  class?: string;
  /** Optional display text; the full URL is available as a tooltip when different. */
  label?: string;
};

/** Opens grounded URLs through external-link confirmation. */
export function SourceUrlLink(props: SourceUrlLinkProps) {
  const href = () => props.url.trim();
  const label = () => props.label?.trim() || href();
  const clickable = () => isExternalLinkHref(href());

  return (
    <Show
      when={clickable()}
      fallback={
        <Show when={href()}>
          {(u) => (
            <span
              class={cn("den-source-url-plain", props.class)}
              data-tip={label() === u() ? undefined : u()}
            >
              {label() || u()}
            </span>
          )}
        </Show>
      }
    >
      <a
        href={href()}
        class={cn("den-source-url-link", props.class)}
        data-testid="source-url-link"
        data-tip={label() === href() ? undefined : href()}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          if (clickSelectedText(e, e.currentTarget)) return;
          void confirmAndOpenExternalLink(href());
        }}
      >
        {label()}
      </a>
    </Show>
  );
}
