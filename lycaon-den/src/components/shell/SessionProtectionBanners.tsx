import { Show, createSignal } from "solid-js";
import type { SessionProtectionState } from "../../api/types.ts";
import { SystemNudge } from "../SystemNudge.tsx";

export const MEDIATION_UNAVAILABLE_COPY =
  "Network mediation is unavailable. Outbound connections are blocked; commands that do not use the network are unaffected.";

export const SANDBOX_BYPASS_COPY =
  "Sandbox protections are turned off for this session. Commands may access your machine and network without mediation.";

export type ProtectionBannerId =
  | "mediation_unavailable"
  | "sandbox_bypass"
  | "boundary_overrides";

// Distinct banners identify unavailable mediation and explicit overrides.
export function SessionProtectionBanners(props: {
  protection: SessionProtectionState | null | undefined;
  onDismiss?: (banner: ProtectionBannerId) => void;
}) {
  const [dismissed, setDismissed] = createSignal<
    Partial<Record<ProtectionBannerId, boolean>>
  >({});
  const dismiss = (id: ProtectionBannerId) => {
    setDismissed((prev) => ({ ...prev, [id]: true }));
    props.onDismiss?.(id);
  };
  const prot = () => props.protection;
  return (
    <div data-testid="session-protection-banners">
      <Show
        when={
          prot()?.mediation_unavailable && !dismissed().mediation_unavailable
        }
      >
        <SystemNudge
          role="status"
          testId="protection-mediation-unavailable"
          title="Network mediation unavailable"
          description={MEDIATION_UNAVAILABLE_COPY}
          onDismiss={() => dismiss("mediation_unavailable")}
        />
      </Show>
      <Show when={prot()?.sandbox_bypass && !dismissed().sandbox_bypass}>
        <SystemNudge
          role="status"
          testId="protection-sandbox-bypass"
          title="Sandbox protections off"
          description={SANDBOX_BYPASS_COPY}
          onDismiss={() => dismiss("sandbox_bypass")}
        />
      </Show>
      <Show
        when={
          !prot()?.sandbox_bypass &&
          !dismissed().boundary_overrides &&
          (prot()?.control_plane_reads_allowed ||
            (prot()?.additional_write_roots?.length ?? 0) > 0)
        }
      >
        <SystemNudge
          role="status"
          testId="protection-boundary-overrides"
          title="Approval boundaries modified"
          description={
            <>
              <Show when={prot()?.control_plane_reads_allowed}>
                Startup settings allow commands to read app configuration without
                a separate approval. Protected key material keeps its own
                restrictions.{" "}
              </Show>
              <Show when={(prot()?.additional_write_roots?.length ?? 0) > 0}>
                Startup settings allow writes to these additional folders without
                a separate approval: {prot()?.additional_write_roots?.join(", ")}.
                Protected paths within them remain restricted.
              </Show>
            </>
          }
          onDismiss={() => dismiss("boundary_overrides")}
        />
      </Show>
    </div>
  );
}

export function sessionProtectionPresent(
  protection: SessionProtectionState | null | undefined,
  dismissed?: Partial<Record<ProtectionBannerId, boolean>>,
): boolean {
  if (!protection) return false;
  const showMediation =
    protection.mediation_unavailable === true &&
    !dismissed?.mediation_unavailable;
  const showBypass =
    protection.sandbox_bypass === true && !dismissed?.sandbox_bypass;
  const showOverrides =
    !protection.sandbox_bypass &&
    !dismissed?.boundary_overrides &&
    (protection.control_plane_reads_allowed === true ||
      (protection.additional_write_roots?.length ?? 0) > 0);
  return showMediation || showBypass || showOverrides;
}
