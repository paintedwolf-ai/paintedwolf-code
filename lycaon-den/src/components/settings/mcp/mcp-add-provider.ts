import { createSignal } from "solid-js";
import type { CreateMcpProviderRequest, McpRecipe } from "../../../api/types.ts";
import type { AppNotice } from "../../../notices/notice-model.ts";
import { applyCredentialWire } from "../../../settings/mcp/mcp-provider-model.ts";

import { classifyMcpHttpUrl } from "../../../settings/mcp/mcp-http-url.ts";

import { type AddDraft, emptyAddDraft, parseEnvText } from "./mcp-connection-draft.ts";
import type { McpSettingsScope } from "./McpSettingsPanel.tsx";

type McpAddProviderOptions = Pick<McpSettingsScope, "currentClient" | "projectId" | "projectLocal" | "catchNotice" | "reloadProviders"> & {
  selectProviderId: (id: string) => void;
};

export function createMcpAddProvider({ currentClient, projectId, projectLocal, catchNotice, reloadProviders, selectProviderId }: McpAddProviderOptions) {
  const [addOpen, setAddOpen] = createSignal(false);
  const [addStep, setAddStep] = createSignal<"pick" | "custom">("pick");
  const [addCustomClass, setAddCustomClass] = createSignal<"local" | "web">(
    "local",
  );
  const [recipes, setRecipes] = createSignal<McpRecipe[]>([]);
  const [addDraft, setAddDraft] = createSignal<AddDraft>(emptyAddDraft());
  const [addError, setAddError] = createSignal<AppNotice | undefined>();
  const [addBusy, setAddBusy] = createSignal(false);
  const addReady = () => {
    const d = addDraft();
    if (!d.id.trim()) return false;
    if (d.transport === "stdio") return Boolean(d.command.trim());
    const loopbackOnly =
      projectLocal() || addCustomClass() === "local";
    const classified = classifyMcpHttpUrl(d.url, loopbackOnly);
    if (!classified.ok) return false;
    if (addCustomClass() === "web" && classified.kind === "loopback") {
      return false;
    }
    return true;
  };

  const openAdd = () => {
    setAddDraft(emptyAddDraft());
    setAddError(undefined);
    setAddStep("pick");
    setAddCustomClass("local");
    setAddOpen(true);
    void (async () => {
      try {
        const catalog = await currentClient().listMcpRecipes(projectId());
        setRecipes(catalog.recipes ?? []);
      } catch (err) {
        setAddError(catchNotice(err));
        setRecipes([]);
      }
    })();
  };

  const closeAdd = () => {
    if (addBusy()) return;
    setAddOpen(false);
    setAddStep("pick");
    setAddCustomClass("local");
  };

  const saveAdd = async () => {
    const d = addDraft();
    setAddBusy(true);
    setAddError(undefined);
    try {
      const id = d.id.trim();
      const body: CreateMcpProviderRequest = {
        source: "custom",
        id,
        enabled: false,
      };
      if (d.transport === "stdio") {
        body.command = d.command.trim();
        const args = d.args
          .trim()
          .split(/\s+/)
          .filter(Boolean);
        if (args.length) body.args = args;
        const env = parseEnvText(d.envText);
        if (env) body.env = env;
      } else {
        const loopbackOnly =
          projectLocal() || addCustomClass() === "local";
        const classified = classifyMcpHttpUrl(d.url, loopbackOnly);
        if (!classified.ok) {
          return;
        }
        if (addCustomClass() === "web" && classified.kind === "loopback") {
          return;
        }
        body.url = classified.url;
        if (addCustomClass() !== "local" && d.token.trim()) {
          body.token = d.token.trim();
          applyCredentialWire(body, d.credentialWire, d.credentialHeader, {
            omitDefault: true,
          });
        }
      }
      await currentClient().createMcpProvider(body, projectId());
      setAddOpen(false);
      setAddStep("pick");
      await reloadProviders();
      selectProviderId(id);
    } catch (err) {
      setAddError(catchNotice(err));
    } finally {
      setAddBusy(false);
    }
  };

  const addRecipe = async (recipe: McpRecipe) => {
    if (recipe.added) {
      setAddOpen(false);
      selectProviderId(recipe.id);
      return;
    }
    setAddBusy(true);
    setAddError(undefined);
    try {
      const created = await currentClient().createMcpProvider({ source: "recipe", recipe_id: recipe.id }, projectId());
      setAddOpen(false);
      setAddStep("pick");
      await reloadProviders();
      selectProviderId(created.id);
    } catch (err) {
      setAddError(catchNotice(err));
    } finally {
      setAddBusy(false);
    }
  };

  return { addOpen, addStep, setAddStep, addCustomClass, setAddCustomClass, recipes, addDraft, setAddDraft, addError, setAddError, addBusy, addReady, openAdd, closeAdd, saveAdd, addRecipe };
}
