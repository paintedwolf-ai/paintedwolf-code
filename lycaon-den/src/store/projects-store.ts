import { createStore } from "solid-js/store";
import type { LycaonClient } from "../api/client.ts";
import type { Project, ProjectEvent } from "../api/types.ts";
import { syncCachedProjectsToDisk } from "../chat/session/session-chat-persist.ts";
import { toProjectSummary, type ProjectSummary } from "../project/project-summary.ts";
import type { CachedProject } from "../../shared/app-state-types.ts";
import { cachedToProject } from "./boot-cache-model.ts";
import {
  type LoadState,
  isLoaded,
  loadFailed,
  loaded,
  unloaded,
} from "./load-state.ts";

export type ProjectsStoreState = {
  /** Projects from the cache or registry. */
  projects: Project[];
  /** Registry read state. */
  registry: LoadState<null>;
};

export type SseBus = {
  onProjectEvent: (cb: (ev: ProjectEvent) => void) => () => void;
};

function parseTs(ts?: string | null): number {
  if (!ts) return 0;
  const n = Date.parse(ts);
  return Number.isFinite(n) ? n : 0;
}

function sortRecentFirst(projects: readonly Project[]): Project[] {
  return [...projects].sort(
    (a, b) => parseTs(b.last_opened_at) - parseTs(a.last_opened_at),
  );
}

export function createProjectsStore(
  getClient: () => LycaonClient | null,
  sse: SseBus,
) {
  const [state, setState] = createStore<ProjectsStoreState>({
    projects: [],
    registry: unloaded(),
  });
  let registryVersion = 0;
  let registryRead: object | undefined;
  let registryClient = getClient();
  const edits = new Map<string, { latest: object; pending: Set<object> }>();
  sse.onProjectEvent((event) => {
    if (event.action === "deleted") {
      drop(event.id);
      return;
    }
    if (event.project) {
      upsert(event.project);
    }
  });

  /** A person's edit shown over host rows until the host answers it. */
  const holds = new Map<string, { patch: Partial<Project>; token: object; base: Project }>();

  function withHold(project: Project): Project {
    const hold = holds.get(project.id);
    if (!hold) return project;
    hold.base = project;
    return { ...project, ...hold.patch };
  }

  function upsert(project: Project): void {
    write(withHold(project));
  }

  function write(project: Project): void {
    registryVersion++;
    const without = state.projects.filter((p) => p.id !== project.id);
    const next = sortRecentFirst([project, ...without]);
    setState("projects", next);
    if (isLoaded(state.registry)) {
      void syncCachedProjectsToDisk(next).catch(() => undefined);
    }
  }

  function drop(id: string): void {
    registryVersion++;
    holds.delete(id);
    edits.delete(id);
    const next = state.projects.filter((p) => p.id !== id);
    setState("projects", next);
    if (isLoaded(state.registry)) {
      void syncCachedProjectsToDisk(next).catch(() => undefined);
    }
  }

  const api = {
    state,
    async hydrate() {
      const client = getClient();
      if (!client) {
        setState("registry", loadFailed(new Error("backend not connected"), state.registry));
        return;
      }
      await api.refresh(client).catch(() => undefined);
    },
    async refresh(client: LycaonClient): Promise<readonly Project[]> {
      const connection = getClient();
      if (connection && connection !== client) return state.projects;
      if (registryClient !== client) {
        registryClient = client;
        holds.clear();
        edits.clear();
        registryVersion++;
      }
      const request = {};
      registryRead = request;
      const isCurrent = () => registryRead === request && getClient() === connection;
      while (isCurrent()) {
        const version = registryVersion;
        try {
          const projects = await client.listProjects();
          if (!isCurrent()) return state.projects;
          // Re-read when events or local edits supersede the list snapshot.
          if (version !== registryVersion) continue;
          api.load(projects);
          return state.projects;
        } catch (err) {
          if (!isCurrent()) return state.projects;
          if (version !== registryVersion) continue;
          setState("registry", loadFailed(err, state.registry));
          throw err;
        }
      }
      return state.projects;
    },
    load(projects: readonly Project[]) {
      registryRead = undefined;
      registryVersion++;
      setState({ projects: sortRecentFirst(projects.map(withHold)), registry: loaded(null) });
      void syncCachedProjectsToDisk(projects).catch(() => undefined);
    },
    /** Pending edits overlay host updates; the latest edit controls settlement. */
    async applyPatch(
      id: string,
      patch: Partial<Project>,
      send: () => Promise<Project>,
    ): Promise<void> {
      const token = {};
      const operation = edits.get(id) ?? { latest: token, pending: new Set<object>() };
      operation.latest = token;
      operation.pending.add(token);
      registryVersion++;
      edits.set(id, operation);
      const client = getClient();
      const current = api.byId(id);
      if (current) {
        const earlier = holds.get(id);
        holds.set(id, {
          patch: { ...earlier?.patch, ...patch }, token, base: earlier?.base ?? current,
        });
        write({ ...current, ...patch });
      }
      const isCurrent = () => edits.get(id) === operation && operation.latest === token && getClient() === client;
      try {
        const updated = await send();
        if (!isCurrent()) {
          const hold = holds.get(id);
          if (edits.get(id) === operation && getClient() === client && hold) {
            hold.base = updated;
            write({ ...updated, ...hold.patch });
          }
          return;
        }
        holds.delete(id);
        upsert(updated);
      } catch (error) {
        const hold = holds.get(id);
        if (isCurrent() && hold?.token === token) {
          holds.delete(id);
          write(hold.base);
        }
        if (isCurrent()) {
          const pending = [...operation.pending].filter((entry) => entry !== token);
          const previous = pending[pending.length - 1];
          if (previous) operation.latest = previous;
        }
        throw error;
      } finally {
        operation.pending.delete(token);
        if (operation.pending.size === 0 && edits.get(id) === operation) edits.delete(id);
      }
    },
    seedFromSnapshot(cached: readonly CachedProject[]) {
      if (cached.length === 0) return;
      // Cached rows do not settle the registry.
      setState("projects", sortRecentFirst(cached.map(cachedToProject)));
    },
    recentFirst(): Project[] {
      return sortRecentFirst(state.projects);
    },
    byId(id: string): Project | undefined {
      return state.projects.find((p) => p.id === id);
    },
    summary(id: string): ProjectSummary | undefined {
      const p = api.byId(id);
      return p ? toProjectSummary(p) : undefined;
    },
    summaries(): ProjectSummary[] {
      return api.recentFirst().map(toProjectSummary);
    },
    upsert,
    drop,
  };

  return api;
}

export type ProjectsStore = ReturnType<typeof createProjectsStore>;
