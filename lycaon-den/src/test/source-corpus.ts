import { readdirSync, readFileSync, statSync } from "node:fs";
import { extname, join, relative, resolve, sep } from "node:path";
import * as ts from "typescript";

export type SourceCorpusOptions = {
  extensions?: readonly string[];
  skipDirectories?: readonly string[];
  excludeTests?: boolean;
};

export type SourceFile = {
  readonly path: string;
  readonly rel: string;
  readonly text: string;
};

export type TypeScriptSourceFile = SourceFile & {
  readonly ast: ts.SourceFile;
};

const defaultSkipDirectories = [".git", "dist", "node_modules", "target", "vendor"];
const sourceCache = new Map<string, SourceCorpus>();
const typeScriptCache = new Map<string, SourceCorpus<TypeScriptSourceFile>>();

export const denRoot = resolve(import.meta.dirname, "../..");
export const denSourceRoot = join(denRoot, "src");

export class SourceCorpus<TFile extends SourceFile = SourceFile> {
  readonly root: string;
  readonly files: readonly TFile[];
  readonly #byRelativePath: ReadonlyMap<string, TFile>;

  constructor(root: string, files: readonly TFile[]) {
    this.root = root;
    this.files = Object.freeze(files.map((file) => Object.freeze({ ...file })));
    this.#byRelativePath = new Map(this.files.map((file) => [file.rel, file]));
  }

  file(rel: string): TFile | undefined {
    return this.#byRelativePath.get(normalizeRelative(rel));
  }

  under(rel: string): readonly TFile[] {
    const prefix = normalizeRelative(rel);
    if (!prefix) return this.files;
    return this.files.filter(
      (file) => file.rel === prefix || file.rel.startsWith(`${prefix}/`),
    );
  }

  select(keep: (file: TFile) => boolean): readonly TFile[] {
    return this.files.filter(keep);
  }
}

export function loadSourceCorpus(
  root: string,
  options: SourceCorpusOptions = {},
): SourceCorpus {
  const normalizedRoot = resolve(root);
  const key = cacheKey(normalizedRoot, options);
  const cached = sourceCache.get(key);
  if (cached) return cached;

  if (!statSync(normalizedRoot).isDirectory()) {
    throw new Error(`source corpus root is not a directory: ${normalizedRoot}`);
  }
  const extensions = normalizedExtensions(options.extensions);
  const skipped = new Set(
    options.skipDirectories ?? defaultSkipDirectories,
  );
  const files: SourceFile[] = [];
  walk(normalizedRoot, normalizedRoot, extensions, skipped, options.excludeTests ?? false, files);
  const corpus = new SourceCorpus(normalizedRoot, files);
  sourceCache.set(key, corpus);
  return corpus;
}

export function loadTypeScriptCorpus(
  root: string,
  options: Omit<SourceCorpusOptions, "extensions"> = {},
): SourceCorpus<TypeScriptSourceFile> {
  const normalizedRoot = resolve(root);
  const sourceOptions = { ...options, extensions: [".ts", ".tsx"] };
  const key = cacheKey(normalizedRoot, sourceOptions);
  const cached = typeScriptCache.get(key);
  if (cached) return cached;

  const source = loadSourceCorpus(normalizedRoot, sourceOptions);
  const files = source.files.map((file): TypeScriptSourceFile => ({
    ...file,
    ast: ts.createSourceFile(
      file.path,
      file.text,
      ts.ScriptTarget.Latest,
      true,
      file.path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
    ),
  }));
  const corpus = new SourceCorpus(normalizedRoot, files);
  typeScriptCache.set(key, corpus);
  return corpus;
}

export function requireNonEmpty<T>(
  label: string,
  values: readonly T[],
): readonly T[] {
  if (values.length === 0) {
    throw new Error(`${label}: source corpus selection is empty`);
  }
  return values;
}

function walk(
  root: string,
  dir: string,
  extensions: ReadonlySet<string>,
  skipped: ReadonlySet<string>,
  excludeTests: boolean,
  out: SourceFile[],
): void {
  const entries = readdirSync(dir, { withFileTypes: true }).sort((a, b) =>
    a.name < b.name ? -1 : a.name > b.name ? 1 : 0,
  );
  for (const entry of entries) {
    if (entry.isDirectory() && skipped.has(entry.name)) continue;
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(root, path, extensions, skipped, excludeTests, out);
      continue;
    }
    if (!entry.isFile()) continue;
    if (extensions.size > 0 && !extensions.has(extname(entry.name))) continue;
    if (excludeTests && /\.(?:test|spec)\.[^.]+$/.test(entry.name)) continue;
    out.push({
      path,
      rel: relative(root, path).split(sep).join("/"),
      text: readFileSync(path, "utf8"),
    });
  }
}

function normalizedExtensions(
  extensions: readonly string[] | undefined,
): ReadonlySet<string> {
  return new Set(
    (extensions ?? []).filter(Boolean).map((extension) =>
      extension.startsWith(".") ? extension : `.${extension}`,
    ),
  );
}

function cacheKey(root: string, options: SourceCorpusOptions): string {
  const extensions = [...normalizedExtensions(options.extensions)].sort();
  const skipped = [
    ...new Set(options.skipDirectories ?? defaultSkipDirectories),
  ].sort();
  return JSON.stringify([root, extensions, skipped, options.excludeTests ?? false]);
}

function normalizeRelative(path: string): string {
  const normalized = path.split(sep).join("/").replace(/^\.\//, "");
  return normalized === "." ? "" : normalized.replace(/^\/+|\/+$/g, "");
}
