import fs from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import type { Plugin } from "vite";

/** Appends JSONL posted to `/__den/scroll-debug` while `VITE_DEN_SCROLL_DEBUG` is 1 or 2. */
export function denScrollDebugPlugin(): Plugin {
  return {
    name: "den-scroll-debug",
    configureServer(server) {
      const enabled = ["1", "2"].includes(process.env.VITE_DEN_SCROLL_DEBUG ?? "");
      const logFile = process.env.VITE_DEN_SCROLL_DEBUG_FILE?.trim();
      if (!enabled || !logFile) return;

      fs.mkdirSync(path.dirname(logFile), { recursive: true });
      if (!fs.existsSync(logFile)) {
        fs.writeFileSync(logFile, "");
      }

      server.middlewares.use("/__den/scroll-debug", (req, res, next) => {
        if (req.method !== "POST") {
          next();
          return;
        }
        const chunks: Buffer[] = [];
        req.on("data", (chunk: Buffer) => {
          chunks.push(chunk);
        });
        req.on("end", () => {
          try {
            const body = Buffer.concat(chunks).toString("utf8");
            if (body.length > 0) {
              fs.appendFileSync(
                logFile,
                body.endsWith("\n") ? body : `${body}\n`,
              );
            }
            res.statusCode = 204;
            res.end();
          } catch (err) {
            res.statusCode = 500;
            res.end(err instanceof Error ? err.message : "write failed");
          }
        });
      });
    },
  };
}

/** Default JSONL path under the development configuration tree. */
export function defaultDenScrollDebugFile(configDir?: string): string {
  const root =
    configDir?.trim() ||
    process.env.LYCAON_CONFIG_DIR?.trim() ||
    path.join(homedir(), ".config", "paintedwolf-dev");
  return path.resolve(root, "debug", "den-scroll.jsonl");
}
