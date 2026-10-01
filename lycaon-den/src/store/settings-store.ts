import { createStore, reconcile } from "solid-js/store";
import { batch } from "solid-js";
import { createProjectionStore } from "./projection-store.ts";
import { errorOf, isResolved } from "./load-state.ts";
import type {
  ModelPolicy,
  ApprovalConfigResponse,
  ProviderKindTemplate,
  ProviderMeta,
  ReviewSettingsResponse,
  SettingsLimitsResponse,
  SettingsPricingResponse,
} from "../api/types.ts";

export interface SettingsState {
  providers: ProviderMeta[];
  /** Canonical labels for provider instances. */
  providerKinds?: ProviderKindTemplate[];
  modelPolicy?: ModelPolicy;
  approvals?: ApprovalConfigResponse;
  limits?: SettingsLimitsResponse;
  effectiveLimits?: {
    projectId: string;
    limits: SettingsLimitsResponse;
  };
  review?: ReviewSettingsResponse;
  pricing?: SettingsPricingResponse;
  error?: string;
}

export const INITIAL_SETTINGS_STATE: Readonly<SettingsState> = {
  providers: [],
  providerKinds: [],
};

export type SettingsStoreActions = {
  setError: (error?: string) => void;
  setProviders: (providers: ProviderMeta[]) => void;
  setProviderKinds: (kinds: ProviderKindTemplate[]) => void;
  setModelPolicy: (policy: ModelPolicy) => void;
  setApprovals: (approvals: ApprovalConfigResponse) => void;
  setLimits: (limits: SettingsLimitsResponse) => void;
  setEffectiveLimits: (
    projectId: string,
    limits: SettingsLimitsResponse,
  ) => void;
  setReview: (review: ReviewSettingsResponse) => void;
  setPricing: (pricing: SettingsPricingResponse) => void;
};

export type SettingsStore = {
  state: SettingsState;
  actions: SettingsStoreActions;
  load: (client: object, panel: string, fetch: () => Promise<Partial<SettingsState>>, refresh?: boolean) => Promise<void>;
  panelReady: (client: object, panel: string) => boolean;
  panelError: (client: object, panel: string) => string | undefined;
  invalidate: () => void;
};

export function createSettingsStore(
  initial: Readonly<SettingsState> = INITIAL_SETTINGS_STATE,
  device?: SettingsStore,
): SettingsStore {
  const [state, setState] = createStore<SettingsState>({ ...initial });
  let currentClient: object | undefined;
  let cache = createProjectionStore<Partial<SettingsState>>();
  const cacheFor = (client: object) => {
    if (client !== currentClient) {
      cache.clear();
      cache = createProjectionStore<Partial<SettingsState>>();
      currentClient = client;
    }
    return cache;
  };
  const invalidate = () => cache.invalidate();

  const actions: SettingsStoreActions = {
    setError(error) {
      setState("error", error);
    },
    setProviders(providers) {
      if (device) return device.actions.setProviders(providers);
      invalidate();
      setState("providers", providers);
    },
    setProviderKinds(kinds) {
      if (device) return device.actions.setProviderKinds(kinds);
      setState("providerKinds", kinds);
    },
    setModelPolicy(policy) {
      invalidate();
      // Full replace removes absent pool rows.
      setState("modelPolicy", reconcile(policy, { key: null }));
    },
    setApprovals(approvals) {
      invalidate();
      // Full replace clears project fields omitted at device scope.
      setState("approvals", reconcile(approvals, { key: null }));
    },
    setLimits(limits) {
      if (device) return device.actions.setLimits(limits);
      invalidate();
      // Full replace clears fields omitted after scope changes.
      setState("limits", reconcile(limits, { key: null }));
    },
    setEffectiveLimits(projectId, limits) {
      setState("effectiveLimits", { projectId, limits });
    },
    setReview(review) {
      invalidate();
      setState("review", review);
    },
    setPricing(pricing) {
      if (device) return device.actions.setPricing(pricing);
      invalidate();
      // Full replace clears the timestamp when tracking is disabled.
      setState("pricing", reconcile(pricing, { key: null }));
    },
  };

  const view = device ? new Proxy(state, {
    get(target, key, receiver) {
      switch (key) {
        case "providers": case "providerKinds": case "limits": case "pricing":
          return device.state[key];
        default:
          return Reflect.get(target, key, receiver);
      }
    },
  }) : state;
  const store: SettingsStore = {
    state: view,
    actions,
    invalidate,
    panelReady: (client, panel) => isResolved(cacheFor(client).get(panel).state()),
    panelError: (client, panel) => errorOf(cacheFor(client).get(panel).state()),
    async load(client, panel, fetch, refresh = false) {
      const requestedCache = cacheFor(client);
      const record = requestedCache.get(panel);
      if (refresh) record.invalidate();
      const release = record.retain();
      try {
        const snapshot = await record.read(fetch);
        if (requestedCache !== cache) return;
        const result = record.state();
        if (snapshot) batch(() => {
          for (const key of Object.keys(snapshot) as (keyof SettingsState)[]) {
            // Each supplied field is a complete host projection, including omitted optional members.
            setState(key, reconcile(snapshot[key], { key: null }) as never);
          }
          if (device) {
            if (snapshot.providers) device.actions.setProviders(snapshot.providers);
          }
          setState("error", undefined);
        });
        else if (result.state === "error") setState("error", result.message);
      } finally {
        release();
        requestedCache.trim();
      }
    },
  };
  return store;
}
