import { For, Show, createMemo, createSignal } from "solid-js";
import type { ExternalAccess } from "../../api/types.ts";
import {
  EXTERNAL_ACCESS_LABEL,
  buildExternalAccessView,
  type ExternalAccessView,
} from "../../chat/tool/external-access-presentation.ts";
import { useNetworkActions } from "../../chat/network-action-context.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import type { TranscriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

export function NetworkChicklet(props: {
  externalAccess: ExternalAccess;
  sessionId?: string;
  disclosureKey?: TranscriptDisclosureKey;
}) {
  const actions = useNetworkActions();
  const [blocking, setBlocking] = createSignal<string | null>(null);
  const { key: disclosureKey, open, onToggle, onSummaryClick } = useTranscriptDisclosure(
    () => props.disclosureKey,
  );

  const view = createMemo((): ExternalAccessView | null =>
    buildExternalAccessView({
      externalAccess: props.externalAccess,
    }),
  );

  const isBlocked = (host: string, allowed: boolean) =>
    !allowed || (actions?.isBlocked(host) ?? false);

  const block = async (host: string) => {
    if (!actions) return;
    setBlocking(host);
    try {
      await actions.blockHost(host);
    } finally {
      setBlocking(null);
    }
  };

  return (
    <Show when={view()} keyed>
      {(v) => (
        <Show when={v.mustShow}>
          <details
            class="den-network-chicklet den-transcript-disclosure-card"
            data-testid="network-chicklet"
            data-visibility={v.visibilitySummary}
            data-disclosure-key={disclosureKey}
            open={open()}
            onToggle={onToggle}
          >
            <summary
              class="den-network-chicklet-summary"
              onClick={onSummaryClick}
              aria-label={`${EXTERNAL_ACCESS_LABEL}: ${v.summary}. ${v.visibilityAnnouncement}`}
            >
              <span class="den-network-chicklet-icon" aria-hidden="true">
                ⇅
              </span>
              <span class="den-network-chicklet-label">{EXTERNAL_ACCESS_LABEL}</span>
              <span class="den-network-chicklet-count">{v.summary}</span>
              <span class="den-tool-chicklet-caret" aria-hidden="true" />
            </summary>
            <div class="den-network-chicklet-body">
              <p
                class="den-network-chicklet-sr-only"
                data-testid="external-access-visibility"
              >
                {v.visibilityAnnouncement}
              </p>
              <Show when={v.fullBypass}>
                <p
                  class="den-network-chicklet-note"
                  data-testid="external-access-bypass"
                >
                  Full sandbox bypass — destinations and machine access are
                  unobserved.
                </p>
              </Show>
              <Show when={v.direct}>
                <p
                  class="den-network-chicklet-note"
                  data-testid="external-access-direct"
                >
                  Direct IP: Destinations unobserved
                </p>
              </Show>
              <Show when={v.declared.length > 0}>
                <div data-testid="external-access-declared">
                  <p class="den-network-chicklet-section-label">
                    Declared destinations (not observed)
                  </p>
                  <ul class="den-network-chicklet-hosts">
                    <For each={v.declared}>
                      {(d) => (
                        <li class="den-network-chicklet-host">
                          <span class="den-network-chicklet-host-name">{d}</span>
                          <span class="den-network-chicklet-host-tag">Declared</span>
                        </li>
                      )}
                    </For>
                  </ul>
                </div>
              </Show>
              <Show when={v.sockets.length > 0}>
                <div data-testid="external-access-sockets">
                  <p class="den-network-chicklet-section-label">
                    Local-service sockets (authority available)
                  </p>
                  <ul class="den-network-chicklet-hosts">
                    <For each={v.sockets}>
                      {(s) => (
                        <li class="den-network-chicklet-host">
                          <span class="den-network-chicklet-host-name">
                            {s.pathLabel}
                          </span>
                          <span class="den-network-chicklet-host-tag">
                            {formatSentenceCase(s.scope)}
                          </span>
                          <span class="den-network-chicklet-sr-only">
                            {s.visibilityLabel}. {s.authorityLabel}
                          </span>
                        </li>
                      )}
                    </For>
                  </ul>
                </div>
              </Show>
              <Show when={v.endpoints.length > 0}>
                <div data-testid="external-access-endpoints">
                  <p class="den-network-chicklet-section-label">
                    Observed endpoints
                  </p>
                  <ul class="den-network-chicklet-hosts">
                    <For each={v.endpoints}>
                      {(ep) => (
                        <li
                          classList={{
                            "den-network-chicklet-host": true,
                            "den-network-chicklet-host--blocked": isBlocked(
                              ep.host,
                              ep.decision === "allow",
                            ),
                          }}
                        >
                          <span class="den-network-chicklet-host-name">
                            {ep.label}
                          </span>
                          <span class="den-network-chicklet-sr-only">
                            {ep.visibilityLabel}
                          </span>
                          <Show
                            when={isBlocked(ep.host, ep.decision === "allow")}
                            fallback={
                              <Show when={actions && ep.host}>
                                <button
                                  type="button"
                                  class="den-network-chicklet-host-block"
                                  data-testid={`network-chicklet-block-${ep.host}`}
                                  disabled={blocking() === ep.host}
                                  onClick={() => void block(ep.host)}
                                >
                                  Block
                                </button>
                              </Show>
                            }
                          >
                            <span class="den-network-chicklet-host-tag">Blocked</span>
                          </Show>
                        </li>
                      )}
                    </For>
                  </ul>
                </div>
              </Show>
              <Show when={v.detections.length > 0}>
                <div data-testid="external-access-detections">
                  <p class="den-network-chicklet-section-label">
                    Detection citation
                  </p>
                  <ul class="den-network-chicklet-hosts">
                    <For each={v.detections}>
                      {(d) => (
                        <li class="den-network-chicklet-host">
                          <span class="den-network-chicklet-host-name">
                            {d.title}
                            {d.level ? ` (${d.level})` : ""}
                          </span>
                          <span class="den-network-chicklet-host-tag">
                            Pattern-based
                          </span>
                          <span class="den-network-chicklet-sr-only">
                            {d.citation}. {d.incompleteNote}
                          </span>
                        </li>
                      )}
                    </For>
                  </ul>
                </div>
              </Show>
            </div>
          </details>
        </Show>
      )}
    </Show>
  );
}
