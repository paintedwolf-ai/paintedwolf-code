import { createSignal, Show } from "solid-js";
import { SurfaceDeck } from "../../primitives/SurfaceDeck.tsx";
import { stubClient } from "../../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  ContributionConfigurationProperty,
  ContributionFrameResponse,
} from "../../../api/types.ts";
import { ExtensionsConfigurationPanel } from "./ExtensionsConfigurationPanel.tsx";

function property(
  over: Partial<ContributionConfigurationProperty> &
    Pick<ContributionConfigurationProperty, "id">,
): ContributionConfigurationProperty {
  return {
    type: "boolean",
    description: "D.",
    scope: ["device"],
    is_default: true,
    ...over,
  } as ContributionConfigurationProperty;
}

function frameWith(
  properties: ContributionConfigurationProperty[],
): ContributionFrameResponse {
  return {
    frame_revision: "rev-1",
    commands: [],
    menus: [],
    keybindings: [],
    binding_defaults: [],
    editor_actions: [],
    configuration: properties,
    requirements: [],
    search_sources: [],
    operations: [],
    themes: [],
    notes: [],
  };
}

type SetConfiguration = (
  req: {
    expected_revision: string;
    packs: Record<string, Record<string, unknown>>;
  },
  target: { scope: "device" | "project"; projectId?: string },
) => Promise<{ revision: string }>;

function clientWith(
  frame: ContributionFrameResponse,
  setConfiguration: ReturnType<typeof vi.fn<SetConfiguration>> = vi.fn<SetConfiguration>(
    async () => ({ revision: "rev-next" }),
  ),
): { client: LycaonClient; setConfiguration: typeof setConfiguration } {
  const client = stubClient({
    getContributions: vi.fn(async () => frame),
    updateExtensionConfiguration: setConfiguration,
  });
  return { client, setConfiguration };
}

function mount(
  client: LycaonClient,
  over: Partial<Parameters<typeof ExtensionsConfigurationPanel>[0]> = {},
) {
  return render(() => (
    <ExtensionsConfigurationPanel
      client={client}
      expectedRevision={() => "rev-token"}
      busy={() => false}
      onCommitted={() => {}}
      onError={() => {}}
      {...over}
    />
  ));
}

describe("ExtensionsConfigurationPanel", () => {
  it("costs no request until its tab is shown", async () => {
    const { client } = clientWith(frameWith([]));
    const [tab, setTab] = createSignal("about");
    render(() => <SurfaceDeck active={tab()}>{(section) => <Show when={section === "settings"} fallback={<p>About extensions</p>}>
        <ExtensionsConfigurationPanel client={client} expectedRevision={() => "rev-token"}
          busy={() => false} onCommitted={() => {}} onError={() => {}} />
      </Show>}</SurfaceDeck>);
    expect(client.getContributions).not.toHaveBeenCalled();
    setTab("settings");
    await waitFor(() => expect(client.getContributions).toHaveBeenCalledOnce());
  });

  it("loads the device contribution frame", async () => {
    const { client } = clientWith(frameWith([]));
    mount(client);
    await waitFor(() =>
      expect(client.getContributions).toHaveBeenCalledWith(),
    );
  });

  it("renders the value in force with a control typed by the declaration", async () => {
    const { client } = clientWith(
      frameWith([
        property({ id: "acme/a:verbose", value: true, is_default: false }),
        property({
          id: "acme/a:depth",
          type: "enum",
          enum: ["quick", "deep"],
          value: "deep",
        }),
      ]),
    );
    mount(client);
    const toggle = await screen.findByTestId(
      "extensions-configuration-input-acme/a:verbose",
    );
    expect((toggle as HTMLInputElement).checked).toBe(true);
    const select = screen.getByTestId("extensions-configuration-input-acme/a:depth");
    expect(select.textContent).toContain("deep");
  });

  it("saves every declaring pack in one atomic device transaction", async () => {
    const setConfiguration = vi.fn<SetConfiguration>(async () => ({
      revision: "rev-next",
    }));
    const { client } = clientWith(
      frameWith([
        property({ id: "acme/a:one", value: false }),
        property({ id: "other/b:two", value: false }),
      ]),
      setConfiguration,
    );
    const onCommitted = vi.fn();
    mount(client, { onCommitted });

    const first = (await screen.findByTestId(
      "extensions-configuration-input-acme/a:one",
    )) as HTMLInputElement;
    first.click();
    const second = screen.getByTestId(
      "extensions-configuration-input-other/b:two",
    ) as HTMLInputElement;
    second.click();
    screen.getByTestId("extensions-configuration-save").click();

    await waitFor(() => expect(onCommitted).toHaveBeenCalled());
    expect(setConfiguration).toHaveBeenCalledTimes(1);
    expect(setConfiguration).toHaveBeenCalledWith(
      {
        expected_revision: "rev-token",
        packs: {
          "acme/a": { one: true },
          "other/b": { two: true },
        },
      },
    );
  });

  it("keeps edits made while an earlier save is in flight", async () => {
    let finish!: (value: { revision: string }) => void;
    const setConfiguration = vi.fn<SetConfiguration>(() => new Promise((resolve) => { finish = resolve; }));
    const { client } = clientWith(frameWith([property({ id: "acme/a:one", value: false })]), setConfiguration);
    mount(client);
    const input = await screen.findByTestId("extensions-configuration-input-acme/a:one") as HTMLInputElement;
    input.click();
    screen.getByTestId("extensions-configuration-save").click();
    input.click();
    finish({ revision: "rev-next" });
    await waitFor(() => expect((screen.getByTestId("extensions-configuration-save") as HTMLButtonElement).disabled).toBe(false));
    expect((screen.getByTestId("extensions-configuration-input-acme/a:one") as HTMLInputElement).checked).toBe(false);
    screen.getByTestId("extensions-configuration-save").click();
    expect(setConfiguration).toHaveBeenLastCalledWith({ expected_revision: "rev-token", packs: { "acme/a": { one: false } } });
    finish({ revision: "rev-final" });
  });

  it("discards staged edits without writing", async () => {
    const setConfiguration = vi.fn<SetConfiguration>(async () => ({
      revision: "rev-next",
    }));
    const { client } = clientWith(
      frameWith([property({ id: "acme/a:one", value: false })]),
      setConfiguration,
    );
    mount(client);
    const input = (await screen.findByTestId(
      "extensions-configuration-input-acme/a:one",
    )) as HTMLInputElement;
    input.click();
    screen.getByTestId("extensions-configuration-discard").click();
    await waitFor(() =>
      expect(
        screen.queryByTestId("extensions-configuration-actions"),
      ).toBeNull(),
    );
    expect(setConfiguration).not.toHaveBeenCalled();
  });
});
