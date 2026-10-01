import { describe, expect, it } from "vitest";
import type { CreateMcpProviderRequest, McpProvider } from "../../api/types.ts";
import {
  applyCredentialWire,
  groupProvidersByClass,
  groupRecipesByClass,
  isRejectedProvider,
  normalizeCredentialWire,
  providersForScope,
  recipesForScope,
  providerLocksCredentialWire,
  providerOffersSignIn,
} from "./mcp-provider-model.ts";

describe("mcp provider model", () => {
  it("detects rejected status", () => {
    expect(
      isRejectedProvider({
        id: "evil",
        enabled: false,
        class: "local",
        tool_loading: "auto",
        status: "rejected",
        last_error: "project_stdio_forbidden",
      }),
    ).toBe(true);
  });

  it("hides Sign in for static-token recipes", () => {
    expect(
      providerOffersSignIn({
        id: "github",
        enabled: true,
        class: "web",
        tool_loading: "auto",
        transport: "http",
        url: "https://api.githubcopilot.com/mcp",
        auth: "static_token",
      }),
    ).toBe(false);
    expect(
      providerOffersSignIn({
        id: "custom",
        enabled: true,
        class: "web",
        tool_loading: "auto",
        transport: "http",
        url: "https://example.test/mcp",
      }),
    ).toBe(true);
    expect(
      providerOffersSignIn({
        id: "loop",
        enabled: true,
        class: "local",
        tool_loading: "auto",
        transport: "http",
        url: "http://127.0.0.1:8765/mcp",
        auth: "none",
      }),
    ).toBe(false);
  });

  it("offers device HTTP authentication only within the provider auth contract", () => {
    const authModes = [undefined, "oauth", "none", "static_token"] as const;
    for (const projectScope of [false, true]) {
      for (const providerClass of ["local", "web"] as const) {
        for (const transport of ["http", "stdio"] as const) {
          for (const auth of authModes) {
            for (const signedIn of [false, true]) {
              const provider: McpProvider = {
                id: "scope-fixture", enabled: false, class: providerClass,
                tool_loading: "auto", transport, auth, signed_in: signedIn,
                url: "http://127.0.0.1:8765/mcp",
              };
              expect(providerOffersSignIn(provider, projectScope)).toBe(
                !projectScope && transport === "http" &&
                (auth === undefined || auth === "oauth"),
              );
            }
          }
        }
      }
    }
  });

  it("hides remote recipes from project add", () => {
    const recipes = [
      {
        id: "github",
        label: "GitHub",
        auth: "oauth" as const,
        class: "web" as const,
        added: false,
        project_ok: false,
      },
      {
        id: "loop",
        label: "Loop",
        auth: "none" as const,
        class: "local" as const,
        added: false,
        project_ok: true,
      },
    ];
    expect(recipesForScope(recipes, true).map((r) => r.id)).toEqual(["loop"]);
    expect(recipesForScope(recipes, false)).toHaveLength(2);
  });

  it("groups installed providers by class and hides web in project scope", () => {
    const rows = [
      {
        id: "playwright",
        enabled: false,
        class: "local" as const,
        tool_loading: "auto" as const,
      },
      {
        id: "github",
        enabled: true,
        class: "web" as const,
        tool_loading: "auto" as const,
      },
      {
        id: "bad",
        enabled: false,
        class: "local" as const,
        tool_loading: "auto" as const,
        status: "rejected" as const,
      },
    ];
    const device = groupProvidersByClass(rows, false);
    expect(device.map((g) => g.kind)).toEqual(["local", "web"]);
    expect(device[0]!.providers.map((p) => p.id)).toEqual(["bad", "playwright"]);
    expect(device[1]!.providers.map((p) => p.id)).toEqual(["github"]);

    const project = groupProvidersByClass(rows, true);
    expect(project.map((g) => g.kind)).toEqual(["local"]);
    expect(project[0]!.providers.map((p) => p.id)).toEqual([
      "bad",
      "playwright",
    ]);
  });

  it("limits project mutations to local providers", () => {
    const rows = [
      { id: "loop", enabled: true, class: "local" as const, tool_loading: "auto" as const },
      { id: "github", enabled: true, class: "web" as const, tool_loading: "auto" as const },
    ];
    expect(providersForScope(rows, true).map((row) => row.id)).toEqual([
      "loop",
    ]);
    expect(providersForScope(rows, false)).toEqual(rows);
  });

  it("groups recipes with Custom homes and project Local-only", () => {
    const recipes = [
      {
        id: "playwright",
        label: "Playwright",
        auth: "none" as const,
        class: "local" as const,
        added: false,
        project_ok: false,
      },
      {
        id: "supabase-local",
        label: "Supabase (local)",
        auth: "none" as const,
        class: "local" as const,
        added: false,
        project_ok: true,
      },
      {
        id: "github",
        label: "GitHub",
        auth: "oauth" as const,
        class: "web" as const,
        added: false,
        project_ok: false,
      },
    ];
    const device = groupRecipesByClass(recipes, false);
    expect(device.map((g) => g.kind)).toEqual(["local", "web"]);
    expect(device[0]!.recipes.map((r) => r.id)).toEqual([
      "playwright",
      "supabase-local",
    ]);
    expect(device[1]!.recipes.map((r) => r.id)).toEqual(["github"]);

    const project = groupRecipesByClass(recipes, true);
    expect(project.map((g) => g.kind)).toEqual(["local"]);
    expect(project[0]!.recipes.map((r) => r.id)).toEqual(["supabase-local"]);
  });

  it("normalizes credential wire and omits default on create", () => {
    expect(normalizeCredentialWire(undefined)).toBe("bearer");
    expect(normalizeCredentialWire("token_token")).toBe("token_token");
    expect(
      providerLocksCredentialWire({
        id: "pagerduty",
        enabled: false,
        class: "web",
        tool_loading: "auto",
        recipe: "pagerduty",
      }),
    ).toBe(true);
    expect(
      providerLocksCredentialWire({
        id: "custom",
        enabled: true,
        class: "web",
        tool_loading: "auto",
        transport: "http",
      }),
    ).toBe(false);
    const create: CreateMcpProviderRequest = { source: "custom", id: "x" };
    applyCredentialWire(create, "bearer", "", { omitDefault: true });
    expect(create.credential_wire).toBeUndefined();
    applyCredentialWire(create, "token_token", "");
    expect(create.credential_wire).toBe("token_token");
  });
});
