import { indentUnit } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function open(doc: string, path: string): EditorState {
  return EditorState.create({
    doc,
    extensions: [languageExtensionForPath(path)!, indentUnit.of("  "), createPrettyExtension({ enabled: true, path })],
  });
}

function breaks(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) out.push(from);
  });
  return out;
}

function writtenOut(state: EditorState): string {
  const decos = state.facet(prettyLayoutDecorations);
  const marks = new Map<number, number>();
  const breakSet = new Set<number>();
  decos.between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) breakSet.add(from);
    else if (value.spec.attributes?.style) {
      const m = /--pretty-indent:\s*(\d+)/.exec(value.spec.attributes.style);
      if (m) marks.set(from, Number(m[1]));
    }
  });
  const doc = state.doc.toString();
  let out = "";
  for (let i = 0; i < doc.length; i++) {
    if (breakSet.has(i)) out += "\n" + " ".repeat(marks.get(i) ?? 0);
    out += doc[i];
  }
  return out;
}

describe("Shell and Dockerfile command-chain layout", () => {
  it("lays out wide Dockerfile RUN chains and CMD arrays and passes second-pass idempotence", () => {
    const pad = "pkg-".repeat(45);
    const df = [
      `RUN apt-get update && apt-get install -y ${pad}curl && rm -rf /var/lib/apt/lists/*`,
      `CMD ["python3", "-m", "uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "${pad}8000"]`,
    ].join("\n");
    const state = open(df, "Dockerfile");
    const pos = breaks(state);
    expect(pos).toContain(df.indexOf("&& apt-get install"));
    expect(pos).toContain(df.indexOf("&& rm -rf"));
    expect(pos).toContain(df.indexOf('"python3"'));
    expect(breaks(open(writtenOut(state), "Dockerfile"))).toEqual([]);
  });

  it("lays out wide Bash pipelines while preserving \\;, ;;, ${var#pat}, and quoted operators", () => {
    const pad = "arg_".repeat(45);
    const sh =
      `find . -name "${pad}*.tmp" -exec rm {} \\; && echo "${pad}a && b | c" | grep -v "\${url#https://}" && case $x in a) echo 1;; esac; echo done\n`;
    const state = open(sh, "deploy.sh");
    const pos = breaks(state);
    expect(pos).not.toContain(sh.indexOf("\\;") + 1);
    expect(pos).not.toContain(sh.indexOf("a && b") + 2);
    expect(pos).not.toContain(sh.indexOf(";;"));
    expect(pos).toContain(sh.indexOf("&& echo"));
    expect(pos).toContain(sh.indexOf("| grep"));
    expect(breaks(open(writtenOut(state), "deploy.sh"))).toEqual([]);
  });
});
