import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { expect } from "vitest";
import {
  denSrc,
  shellSource,
  rgForbidden,
  PROJECTS_REGISTRY_ALLOWLIST,
  type InvariantEntry,
} from "./common.ts";

function assertCache01(): void {
  const src = readSourceText(join(denSrc, "store/app-state.ts"));
  expect(src).not.toMatch(/^\s*projects: Project\[\]/m);
  expect(src).not.toMatch(/setProjects/);
}

function assertCache03(): void {
  const registryHits = rgForbidden("projectsRegistry", join(denSrc, "src"), {
    allowGlobs: PROJECTS_REGISTRY_ALLOWLIST,
  });
  expect(
    registryHits,
    `projectsRegistry outside ${PROJECTS_REGISTRY_ALLOWLIST.join(", ")}\n${registryHits.join("\n")}`,
  ).toEqual([]);

  const shell = shellSource();
  expect(shell).toMatch(/projects=\{props\.projects\}/);

  const chatView = readSourceText(join(denSrc, "components/chatview/ChatView.tsx"));
  expect(chatView).toMatch(/projects:\s*ProjectsStore/);
}

export const CACHE_INVARIANTS: InvariantEntry[] = [
  {
    id: "INV-CACHE-01",
    class: "forbidden",
    structural: assertCache01,
    note: "Project state has no projects cache",
  },
  {
    id: "INV-CACHE-02",
    class: "forbidden",
    pattern: "projectIdForPath\\(appStore\\.state\\.projects",
    roots: join(denSrc, "src"),
  },
  {
    id: "INV-CACHE-03",
    class: "required",
    structural: assertCache03,
    note: "projectsRegistry only in app-connection; UI threads ProjectsStore",
  },
];
