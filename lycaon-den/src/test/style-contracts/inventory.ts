import { loadSourceCorpus } from "../source-corpus.ts";
import { readSourceText, stylesheetDependencies } from "../stylesheet-source.ts";
import { join } from "node:path";


export function walkFiles(dir: string, predicate: (path: string) => boolean): string[] {
  return loadSourceCorpus(dir).files.map((file) => file.path).filter(predicate);
}

export function stylesheetFamilyFiles(denSrc: string, pattern: RegExp): string[] {
  const entrypoints = walkFiles(denSrc, (path) => pattern.test(path));
  return [...new Set(entrypoints.flatMap((file) => [...stylesheetDependencies(file)]))];
}

export function readDenStylesheetInventory(denSrc: string): {
  cssFiles: string[];
  domainCssFiles: string[];
  tailwindCss: string;
} {
  const cssFiles = walkFiles(denSrc, (p) => p.endsWith(".css") && !p.includes("node_modules"));
  const domainEntrypoints = cssFiles.filter((p) => /-domain\.css$/.test(p) || /-art\.css$/.test(p));
  const domainCssFiles = [...new Set(domainEntrypoints.flatMap((file) => [...stylesheetDependencies(file)]))];
  const tailwindCss = readSourceText(join(denSrc, "tailwind.css"), "utf8");
  return { cssFiles, domainCssFiles, tailwindCss };
}
