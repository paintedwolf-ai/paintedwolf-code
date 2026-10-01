import { createEffect, createSignal, onCleanup } from "solid-js";
import type { McpProvider } from "../../../api/types.ts";
import type { AppNotice } from "../../../notices/notice-model.ts";
import type { McpSettingsScope } from "./McpSettingsPanel.tsx";

type McpSignInOptions = Pick<McpSettingsScope, "currentClient" | "projectId" | "live" | "providerWriter" | "reloadProviders" | "catchNotice" | "setOperationError">;

const OAUTH_POLL_MS = 1500;

export function createMcpSignIn({ currentClient, projectId, live,
  providerWriter, reloadProviders, catchNotice, setOperationError }: McpSignInOptions) {
  const [oauthBusyId, setOauthBusyId] = createSignal<string | undefined>();
  const [oauthPending, setOauthPending] = createSignal<
    { id: string; state: string } | undefined
  >();
  const [oauthCode, setOauthCode] = createSignal("");
  const [oauthError, setOauthError] = createSignal<AppNotice | undefined>();
  // Code entry remains available when loopback does not complete.
  const [oauthManual, setOauthManual] = createSignal(false);
  const startSignIn = async (provider: McpProvider) => {
    setOauthBusyId(provider.id);
    setOauthError(undefined);
    setOauthManual(false);
    try {
      const started = await currentClient().startMcpOAuth(provider.id);
      setOauthPending({ id: provider.id, state: started.state });
      setOauthCode("");
      if (typeof window !== "undefined" && started.authorize_url) {
        window.open(started.authorize_url, "_blank", "noopener,noreferrer");
      }
    } catch (err) {
      setOperationError(catchNotice(err));
    } finally {
      setOauthBusyId(undefined);
    }
  };

  const cancelSignIn = async () => {
    const pending = oauthPending();
    if (!pending || oauthBusyId()) return;
    setOauthBusyId(pending.id);
    setOauthError(undefined);
    try {
      await currentClient().cancelMcpOAuth(pending.id, { state: pending.state });
      if (oauthPending() === pending) {
        setOauthPending(undefined);
        setOauthCode("");
        setOauthManual(false);
      }
    } catch (err) {
      setOauthError(catchNotice(err));
    } finally {
      setOauthBusyId(undefined);
    }
  };

  const completeSignIn = async () => {
    const setProviders = providerWriter();
    const pending = oauthPending();
    if (!pending) return;
    setOauthBusyId(pending.id);
    setOauthError(undefined);
    try {
      const updated = await currentClient().completeMcpOAuth(pending.id, {
        code: oauthCode().trim(),
        state: pending.state,
      });
      setProviders((prev) =>
        prev.map((s) => (s.id === updated.id ? updated : s)),
      );
      setOauthPending(undefined);
      setOauthCode("");
    } catch (err) {
      setOauthError(catchNotice(err));
    } finally {
      setOauthBusyId(undefined);
    }
  };

  const signOut = async (provider: McpProvider) => {
    setOauthBusyId(provider.id);
    setOperationError(undefined);
    try {
      await currentClient().revokeMcpOAuth(provider.id);
      await reloadProviders();
    } catch (err) {
      setOperationError(catchNotice(err));
    } finally {
      setOauthBusyId(undefined);
    }
  };

  const observeSignIn = () => {
    // Close the dialog when the host reports a completed sign-in.
    createEffect(() => {
        const pending = oauthPending();
        if (!live() || !pending || typeof window === "undefined") return;
        const client = currentClient();
        const pid = projectId();
        const setProviders = providerWriter();
        let stopped = false;
        const timer = window.setInterval(() => {
          void (async () => {
            if (stopped) return;
            try {
              const list = await client.listMcpProviders(pid);
              if (stopped) return;
              setProviders(list);
              if (list.some((s) => s.id === pending.id && s.signed_in)) {
                setOauthPending(undefined);
                setOauthCode("");
                setOauthManual(false);
              }
            } catch {
              // Retry transient poll failures.
            }
          })();
        }, OAUTH_POLL_MS);
        onCleanup(() => {
          stopped = true;
          window.clearInterval(timer);
        });
    });
  };

  return { oauthBusyId, oauthPending, oauthCode, setOauthCode, oauthError, oauthManual,
    setOauthManual, startSignIn, cancelSignIn, completeSignIn, signOut, observeSignIn };
}
