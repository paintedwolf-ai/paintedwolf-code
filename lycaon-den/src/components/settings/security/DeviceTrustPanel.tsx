import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { For, Show, createSignal, type JSX } from "solid-js";
import type { TrustSurfaceSetting } from "../../../api/types.ts";
import type { LycaonClient } from "../../../api/client.ts";
import { TRUST_COPY, trustSurfaceHint } from "../../../settings/security/trust-copy.ts";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { SettingsTextLink } from "../SettingsTextLink.tsx";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { noticeFromCaught, type AppNotice } from "../../../notices/notice-model.ts";
import { APP_SCOPE } from "../../../notices/notice-scope.ts";

type Props = {
  client: LycaonClient;
  /** Opens trust settings for the active project. */
  onOpenProjectTrust?: () => void;
};

/** Device-wide project trust controls. */
export function DeviceTrustPanel(props: Props) {
  const query = createSurfaceQuery({
    name: "device-trust",
    source: () => ({ client: props.client, key: "device" }),
    load: ({ client }) => client.getTrustSettings(),
  });
  const surfaces = () => query.value()?.surfaces ?? [];
  const [savingId, setSavingId] = createSignal<string | null>(null);
  const [actionError, setError] = createSignal<AppNotice | undefined>();
  const error = () => actionError() ?? (query.error() ? catchNotice(query.cause(), TRUST_COPY.loadError) : undefined);
  const catchNotice = (err: unknown, fallback: string) =>
    noticeFromCaught(err, APP_SCOPE, { title: fallback, message: fallback });

  const toggle = (surface: TrustSurfaceSetting, enabled: boolean) => {
    const target = query.capture();
    setSavingId(surface.id);
    setError(undefined);
    void props.client
      .updateTrustSettings({ enabled: { [surface.id]: enabled } })
      .then((res) => target.publish(res))
      .catch((err) => setError(catchNotice(err, TRUST_COPY.saveError)))
      .finally(() => setSavingId(null));
  };

  return (
    <section class="den-settings-section" data-testid="device-trust-settings">
      <p class="den-settings-hint" data-testid="device-trust-intro">
        {TRUST_COPY.sectionIntroBefore}
        {pathOrText({
          label: TRUST_COPY.sectionIntroPath,
          onOpen: props.onOpenProjectTrust,
          testId: "device-trust-open-project",
        })}
        {TRUST_COPY.sectionIntroAfter}
      </p>
      <InlineNotice notice={error()} testId="device-trust-error" />
      <Show
        when={query.value() !== undefined}
        fallback={
          <Show when={query.showLoading()}>
            <p class="den-settings-hint" data-testid="device-trust-loading">Loading…</p>
          </Show>
        }
      >
        <div class="den-settings-pref-group">
          <For each={surfaces()}>
            {(surface) => (
              <div
                class="den-settings-pref-row"
                data-testid={`device-trust-row-${surface.id}`}
                data-group={surface.group}
              >
                <div class="den-settings-pref-copy">
                  <span class="den-settings-pref-label">{surface.label}</span>
                  <p class="den-settings-hint">{trustSurfaceHint(surface.id)}</p>
                </div>
                <DenCheckbox
                  checked={surface.enabled}
                  disabled={savingId() === surface.id}
                  data-testid={`device-trust-toggle-${surface.id}`}
                  onChange={(e) => toggle(surface, e.currentTarget.checked)}
                >
                  <span class="sr-only">{surface.label}</span>
                </DenCheckbox>
              </div>
            )}
          </For>
        </div>
      </Show>
    </section>
  );
}

function pathOrText(args: { label: string; onOpen?: () => void; testId: string }): JSX.Element {
  const open = args.onOpen;
  if (!open) return args.label;
  return (
    <SettingsTextLink testId={args.testId} onClick={open}>
      {args.label}
    </SettingsTextLink>
  );
}
