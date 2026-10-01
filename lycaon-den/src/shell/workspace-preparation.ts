import { createPreparation } from "../ui/presentation.ts";

const workspaces = new Map<string, ReturnType<typeof createWorkspacePreparation>>();

function createWorkspacePreparation() {
  const preparation = createPreparation();
  const attempts = new Map<string, () => void>();
  return {
    ...preparation,
    async run<T>(name: string, load: () => Promise<T>): Promise<T> {
      attempts.get(name)?.();
      const release = preparation.register(name, () => false);
      attempts.set(name, release);
      try {
        return await load();
      } finally {
        release();
        if (attempts.get(name) === release) attempts.delete(name);
      }
    },
  };
}

/** Preparation groups are isolated by project. */
export function workspacePreparation(projectId: string) {
  let workspace = workspaces.get(projectId);
  if (!workspace) {
    workspace = createWorkspacePreparation();
    workspaces.set(projectId, workspace);
    for (const [key, candidate] of workspaces) {
      if (workspaces.size <= 64) break;
      if (key !== projectId && candidate.empty()) workspaces.delete(key);
    }
  }
  return workspace;
}
