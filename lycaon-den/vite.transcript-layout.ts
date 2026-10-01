import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import type { Plugin } from "vite";

/** Renderer changes invalidate derived heights; no source text ships to the client. */
export function transcriptLayoutRevision(root: string): string {
  const hash = createHash("sha256");
  const visit = (directory: string) => {
    for (const entry of readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = path.join(directory, entry.name);
      if (entry.isDirectory()) visit(file);
      else if (/\.(?:css|tsx?|woff2?|ttf|otf)$/.test(entry.name) && !/\.(?:test|spec)\./.test(entry.name)) {
        hash.update(path.relative(root, file)).update("\0").update(readFileSync(file)).update("\0");
      }
    }
  };
  visit(path.join(root, "src"));
  visit(path.join(root, "shared"));
  visit(path.join(root, "public/fonts"));
  hash.update(readFileSync(path.join(root, "bun.lock")));
  return hash.digest("hex");
}

export function transcriptLayoutPlugin(root: string): Plugin {
  const runtime = path.join(root, "src/chat/transcript/layout/transcript-layout-revision.ts");
  let revision = transcriptLayoutRevision(root);
  return {
    name: "transcript-layout",
    enforce: "pre",
    transform(code, id) {
      if (id.split("?")[0] !== runtime) return;
      return { code: code.replace('"__TRANSCRIPT_LAYOUT_REVISION__"', JSON.stringify(revision)), map: null };
    },
    hotUpdate(context) {
      if (this.environment.name !== "client") return;
      if (!context.file.startsWith(path.join(root, "src") + path.sep) &&
          !context.file.startsWith(path.join(root, "shared") + path.sep) &&
          !context.file.startsWith(path.join(root, "public/fonts") + path.sep) &&
          context.file !== path.join(root, "bun.lock")) return;
      const next = transcriptLayoutRevision(root);
      if (next === revision) return;
      revision = next;
      // Reloads and live updates share the same generation.
      const module = this.environment.moduleGraph.getModuleById(runtime);
      if (module) this.environment.moduleGraph.invalidateModule(module);
      this.environment.hot.send({ type: "custom", event: "transcript-layout-revision", data: revision });
    },
  };
}
