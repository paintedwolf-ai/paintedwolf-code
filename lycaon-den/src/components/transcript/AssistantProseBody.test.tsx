import { createEffect, createSignal } from "solid-js";
import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { AssistantProseBody } from "../../components/transcript/AssistantProseBody.tsx";
import { buildProseNavigationIndex } from "../../chat/markdown/prose-path-opens.ts";

describe("AssistantProseBody", () => {
  it("renders all markdown immediately and preserves the node on unchanged updates", () => {
    const [source, setSource] = createSignal("## Ready\n\n" + "Complete answer. ".repeat(200));
    const { container } = render(() => <AssistantProseBody source={source()} />);
    const prose = container.querySelector(".assistant-prose")!;
    expect(prose.textContent).toContain("Complete answer. ".repeat(200).trim());
    expect(prose.querySelector("h2")?.textContent).toBe("Ready");
    const heading = prose.firstElementChild;
    setSource(source());
    expect(prose.firstElementChild).toBe(heading);
    setSource("## Updated\n\nAll new text is ready.");
    expect(prose.textContent).toContain("All new text is ready.");
  });

  it("has its prose in place before effects created ahead of it run", () => {
    // A transcript row measures itself from an effect created before its body.
    let measured = "";
    render(() => {
      let row: HTMLDivElement | undefined;
      createEffect(() => {
        measured = row?.textContent ?? "";
      });
      return (
        <div ref={row}>
          <AssistantProseBody source={"Measured **at mount**."} />
        </div>
      );
    });
    expect(measured).toContain("Measured at mount.");
  });

  it("renders tables and code at full size on the first paint and on remount", () => {
    const source = "| Name | Value |\n| --- | --- |\n| Answer | 42 |\n\n```ts\nconst answer = 42;\n```";
    const mount = () => render(() => <AssistantProseBody source={source} />);
    const first = mount();
    expect(first.container.querySelector("td")?.textContent).toBe("Answer");
    expect(first.container.querySelector("pre")?.textContent).toContain("const answer = 42;");
    first.unmount();
    const second = mount();
    expect(second.container.querySelector("td")?.textContent).toBe("Answer");
    expect(second.container.querySelector("pre")?.textContent).toContain("const answer = 42;");
  });

  it("renders host-validated file-path links as source navigation buttons", async () => {

    const navigation = buildProseNavigationIndex([
      {
        id: "ref-1", syntax: "link", status: "resolved", explicit: true, mention: "src/foo.ts:7",
        project_id: "p1",
        root_id: "root-1",
        path: "src/foo.ts",
        entry_kind: "file",
      },
    ]);
    const { container } = render(() => (
      <AssistantProseBody
        source={"See [a](src/foo.ts:7)"}
        projectId="p1"
        navigation={navigation}

      />
    ));
    await Promise.resolve();
    const button = container.querySelector(".den-source-path-link");
    expect(button).not.toBeNull();
    expect(button?.getAttribute("data-den-source-path")).toBe("src/foo.ts");
    expect(button?.getAttribute("data-den-source-line")).toBe("7");
    expect(button?.getAttribute("data-den-project-root-id")).toBe("root-1");
  });

  it("leaves an unvalidated assistant-authored project link plain", async () => {

    const { container } = render(() => (
      <AssistantProseBody
        source={"See [missing](packs/code-hosting/)."}
        projectId="p1"

      />
    ));
    await Promise.resolve();
    expect(container.querySelector(".den-source-path-link")).toBeNull();
    expect(container.querySelector(".den-source-path-plain")?.textContent).toBe(
      "missing",
    );
  });

  const renderAmbiguousReference = async () => {
    const navigation = buildProseNavigationIndex([
      {
        id: "ref-1", syntax: "link", status: "ambiguous", explicit: true, mention: "src/foo.ts:7",
        project_id: "p1",
        root_id: "root-1",
        path: "src/foo.ts",
        entry_kind: "file",
        candidates: [
          { project_id: "p1", root_id: "root-1", path: "src/foo.ts", entry_kind: "file" },
          { project_id: "p1", root_id: "root-2", path: "src/foo.ts", entry_kind: "file" },
        ],
      },
    ]);
    const { container } = render(() => (
      <>
        <p class="other-message">Earlier message</p>
        <AssistantProseBody
          source={"See [label](src/foo.ts:7)"}
          projectId="p1"
          navigation={navigation}
        />
      </>
    ));
    await Promise.resolve();
    const button = container.querySelector(".den-source-path-link") as HTMLElement;
    expect(button).not.toBeNull();
    return { container, button };
  };

  const click = async (button: HTMLElement) => {
    button.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 }));
    await Promise.resolve();
    await Promise.resolve();
  };

  it("does not open a reference when the click ends selecting its text", async () => {
    const { button } = await renderAmbiguousReference();
    document.getSelection()!.selectAllChildren(button);
    try {
      await click(button);
      expect(document.querySelector("[role='dialog']")).toBeNull();
    } finally {
      document.getSelection()!.removeAllRanges();
    }
  });

  it("opens a reference while other text stays selected", async () => {
    const { container, button } = await renderAmbiguousReference();
    document.getSelection()!.selectAllChildren(container.querySelector(".other-message")!);
    try {
      await click(button);
      expect(document.querySelector("[role='dialog']")).not.toBeNull();
    } finally {
      document.getSelection()!.removeAllRanges();
    }
  });
});
