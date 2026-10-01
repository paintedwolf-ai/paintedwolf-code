import { For, Show } from "solid-js";
import type { ProjectTrustSurface, TrustSurfaceItem } from "../../api/types.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { TRUST_COPY, trustCountLabel, trustSurfaceHint } from "../../settings/security/trust-copy.ts";
import {
  trustItemTree,
  type TrustItemTreeNode,
  type TrustTreeRoot,
} from "../../settings/security/trust-item-tree.ts";
import { SettingsListGroup } from "../settings/SettingsListGroup.tsx";
import { SettingsListRow } from "../settings/SettingsListRow.tsx";

type Props = {
  projectId: string;
  projectRoots: readonly TrustTreeRoot[];
  surface: ProjectTrustSurface;
};

export function TrustSurfaceDetail(props: Props) {
  const itemLabel = (item: TrustSurfaceItem): string | undefined => {
    if (item.detail) return item.detail;
    if (item.lines) return TRUST_COPY.detailLines(item.lines);
    return undefined;
  };

  const openItem = (item: TrustSurfaceItem) => {
    void openSourceLocation(
      {
        projectId: props.projectId,
        rootId: item.root_id,
        path: item.path,
        intent: "permanent",
      },
    );
  };

  const itemRow = (item: TrustSurfaceItem, index: number, label = item.name) => (
    <SettingsListRow
      testId={`project-trust-detail-item-${props.surface.id}-${index}`}
      aria-label={`${item.path}. ${TRUST_COPY.detailOpenFile}`}
      primary={label}
      secondary={itemLabel(item)}
      status={
        <span class="den-settings-hint">{TRUST_COPY.detailOpenFile}</span>
      }
      onSelect={() => openItem(item)}
    />
  );

  const agentsTree = () => trustItemTree(props.surface.items, props.projectRoots);

  return (
    <article class="den-settings-detail">
      <header class="den-settings-detail__header">
        <div>
          <h3>{props.surface.label}</h3>
          <p>{trustSurfaceHint(props.surface.id)}</p>
        </div>
        <span class="trust-surface-row__count">
          {trustCountLabel(props.surface.id, props.surface.count)}
        </span>
      </header>

      <SettingsListGroup
        label={
          props.surface.id === "agents_md"
            ? TRUST_COPY.detailAgentsFiles
            : TRUST_COPY.detailItems
        }
        meta={String(props.surface.items.length)}
        testId={`project-trust-detail-group-${props.surface.id}`}
      >
        <Show
          when={props.surface.id === "agents_md"}
          fallback={
            <For each={props.surface.items}>
              {(item, index) => itemRow(item, index())}
            </For>
          }
        >
          <TrustItemTree
            nodes={agentsTree()}
            renderItem={(node) => itemRow(node.item, node.sourceIndex, node.name)}
          />
        </Show>
      </SettingsListGroup>
    </article>
  );
}

function TrustItemTree(props: {
  nodes: TrustItemTreeNode[];
  renderItem: (node: Extract<TrustItemTreeNode, { kind: "item" }>) => ReturnType<
    typeof SettingsListRow
  >;
}) {
  return (
    <ul class="trust-tree">
      <For each={props.nodes}>
        {(node) => (
          <Show
            when={node.kind === "folder" ? node : undefined}
            keyed
            fallback={
              <li>
                {props.renderItem(node as Extract<TrustItemTreeNode, { kind: "item" }>)}
              </li>
            }
          >
            {(folder) => (
              <li data-trust-folder={folder.path}>
                <div class="den-settings-list-group__head">
                  <div class="den-settings-list-group__title">
                    <h4 class="den-settings-subhead">{folder.name}</h4>
                  </div>
                </div>
                <div class="trust-tree__branch">
                  <TrustItemTree nodes={folder.children} renderItem={props.renderItem} />
                </div>
              </li>
            )}
          </Show>
        )}
      </For>
    </ul>
  );
}
