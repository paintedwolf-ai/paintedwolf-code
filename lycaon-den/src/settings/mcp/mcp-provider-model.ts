import type {
  CreateMcpProviderRequest,
  McpCredentialWire,
  McpRecipe,
  McpProvider,
  UpdateMcpProviderRequest,
} from "../../api/types.ts";

export type McpProviderGroupKind = "local" | "web";

export type McpProviderListGroup = {
  kind: McpProviderGroupKind;
  label: string;
  providers: McpProvider[];
};

export type McpRecipeListGroup = {
  kind: McpProviderGroupKind;
  label: string;
  recipes: McpRecipe[];
};

export function isRejectedProvider(provider: McpProvider): boolean {
  return provider.status === "rejected";
}

export function catalogProviders(providers: McpProvider[]): McpProvider[] {
  return providers.filter((provider) => !isRejectedProvider(provider));
}

export function rejectedProviders(providers: McpProvider[]): McpProvider[] {
  return providers.filter(isRejectedProvider);
}

export function recipesForScope(
  recipes: McpRecipe[],
  projectScope: boolean,
): McpRecipe[] {
  if (!projectScope) return recipes;
  return recipes.filter((recipe) => recipe.project_ok);
}

export function providersForScope(
  providers: McpProvider[],
  projectScope: boolean,
): McpProvider[] {
  if (!projectScope) return providers;
  return providers.filter((provider) => provider.class === "local");
}

/** Group installed providers by host class. */
export function groupProvidersByClass(
  providers: McpProvider[],
  projectScope: boolean,
): McpProviderListGroup[] {
  const ordered = [
    ...rejectedProviders(providers),
    ...catalogProviders(providers),
  ];
  const local: McpProvider[] = [];
  const web: McpProvider[] = [];
  for (const row of ordered) {
    if (row.class === "local") local.push(row);
    else if (!projectScope) web.push(row);
  }
  const groups: McpProviderListGroup[] = [];
  if (local.length) {
    groups.push({ kind: "local", label: "Local", providers: local });
  }
  if (web.length) {
    groups.push({ kind: "web", label: "Web / hosted", providers: web });
  }
  return groups;
}

/** Group add recipes and keep a home for Custom local. */
export function groupRecipesByClass(
  recipes: McpRecipe[],
  projectScope: boolean,
): McpRecipeListGroup[] {
  const scoped = recipesForScope(recipes, projectScope);
  const local = scoped.filter((r) => r.class === "local");
  const web = scoped.filter((r) => r.class === "web");
  const groups: McpRecipeListGroup[] = [
    { kind: "local", label: "Local", recipes: local },
  ];
  if (!projectScope) {
    groups.push({ kind: "web", label: "Web / hosted", recipes: web });
  }
  return groups;
}

export function providerOffersSignIn(
  provider: McpProvider,
  projectScope = false,
): boolean {
  if (projectScope) return false;
  if (provider.transport && provider.transport !== "http") return false;
  if (provider.transport !== "http" && !provider.url) return false;
  return provider.auth !== "static_token" && provider.auth !== "none";
}

export function normalizeCredentialWire(
  raw?: string,
): McpCredentialWire {
  if (raw === "token_token" || raw === "header") return raw;
  return "bearer";
}

export function providerLocksCredentialWire(provider: McpProvider): boolean {
  return Boolean(provider.recipe);
}

export function applyCredentialWire(
  body: CreateMcpProviderRequest | UpdateMcpProviderRequest,
  wire: McpCredentialWire,
  header: string,
  opts: { omitDefault?: boolean } = {},
): void {
  if (opts.omitDefault && wire === "bearer") return;
  body.credential_wire = wire;
  if (wire === "header") {
    const name = header.trim();
    if (name) body.credential_header = name;
  }
}
