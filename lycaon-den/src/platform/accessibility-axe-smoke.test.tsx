/** Runs WCAG A/AA checks on representative fixtures. */
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import axe from "axe-core";
import { SessionTranscript } from "../components/transcript/transcript-viewport-test-harness.tsx";
import type { Message, SearchReplacePreviewResponse } from "../api/types.ts";
import { toolApprovalFixture } from "../chat/checkpoint/approval-test-fixtures.ts";

const axeRunOnly = {
  type: "tag" as const,
  values: ["wcag2a", "wcag2aa"],
};

async function assertNoAxeViolations(root: HTMLElement): Promise<void> {
  const results = await axe.run(root, { runOnly: axeRunOnly });
  const summary = results.violations
    .map(
      (v) =>
        `${v.id} (${v.impact ?? "n/a"}): ${v.help} — ${v.nodes
          .map((n) => n.target.join(" "))
          .join("; ")}`,
    )
    .join("\n");
  expect(results.violations, summary || "axe violations").toEqual([]);
}

describe("axe smoke — macOS accessibility", () => {
  it("transcript with user + assistant articles", async () => {
    const messages: Message[] = [
      {
        id: "u1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello from axe smoke",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "reply from axe smoke",
        created_at: "2026-01-01T00:00:01Z",
      },
    ];
    const { container, unmount } = render(() => (
      <SessionTranscript layout="chat" messages={messages} />
    ));
    try {
      await assertNoAxeViolations(container);
    } finally {
      unmount();
    }
  });

  it("high-risk approval card fixture", async () => {
    const { ApprovalCard } = await import("../components/checkpoint/ApprovalCard.tsx");
    const checkpoint = {
      checkpointId: "chk-axe-hr",
      sessionId: "sess-axe",
      kind: "tool_approval" as const,
      status: "pending" as const,
      issuedAt: "t",
      tool_approval: toolApprovalFixture({
        command: "install helper",
        subject: {
          kind: "write_root_set",
          title: "Allow write access",
          targets: [{ kind: "write_root", label: "/Users/me/Library" }],
        },
        presentation: {
          consequence_band: "high_risk",
          consequence_code: "write_root",
        },
      }),
    };
    const { container, unmount } = render(() => (
      <ApprovalCard
        checkpoint={checkpoint}
        onToolApproval={() => undefined}
        onContentApply={() => undefined}
      />
    ));
    try {
      await assertNoAxeViolations(container);
    } finally {
      unmount();
    }
  });

  it("Settings Editor panel", async () => {
    const { EditorSettingsPanel } = await import(
      "../components/settings/appearance/EditorSettingsPanel.tsx"
    );
    const { container, unmount } = render(() => <EditorSettingsPanel />);
    try {
      const root = container.querySelector(
        '[data-testid="editor-settings-panel"]',
      ) as HTMLElement | null;
      expect(root).not.toBeNull();
      await assertNoAxeViolations(root!);
    } finally {
      unmount();
    }
  });

  it("search replace preview chrome", async () => {
    const { SearchReplacePreview } = await import(
      "../components/search/SearchReplacePreview.tsx"
    );
    const { initReplaceSelection } = await import(
      "../search/search-replace-selection.ts"
    );
    const preview: SearchReplacePreviewResponse = {
      state: "ready",
      issues: [],
      truncated: false,
      files: [
        {
          root_id: "r1",
          path: "src/a.ts",
          sha256: "abc",
          hunks: [{ line: 2, end_line: 2, before: "foo", after: "bar", context: "code" as const }],
        },
      ],
    };
    const { container, unmount } = render(() => (
      <SearchReplacePreview
        previewCurrent={true}
        preview={preview}
        selection={initReplaceSelection(preview.files)}
        onSelectionChange={() => undefined}
        applying={false}
        applySummary={null}
        onApply={() => undefined}
        onApplyFile={() => undefined}
        onCancel={() => undefined}
        onSearchAgain={() => undefined}
        applyAllDisabled={false}
      />
    ));
    try {
      const root = container.querySelector(
        '[data-testid="search-replace-preview"]',
      ) as HTMLElement | null;
      expect(root).not.toBeNull();
      await assertNoAxeViolations(root!);
    } finally {
      unmount();
    }
  });

  it("Files Move to trash confirm dialog", async () => {
    const { MoveToTrashDialog } = await import(
      "../files/commands/MoveToTrashDialog.tsx"
    );
    const { container, unmount } = render(() => (
      <MoveToTrashDialog
        state={{
          operationId: "trash-operation",
          rootId: "r1",
          path: "notes.md",
          name: "notes.md",
          isDir: false,
          body: '"notes.md" will move to the Trash.',
        }}
        onCancel={() => undefined}
        onConfirm={() => undefined}
      />
    ));
    try {
      const dialog = document.querySelector(
        '[data-testid="files-trash-dialog"]',
      ) as HTMLElement | null;
      expect(dialog).not.toBeNull();
      await assertNoAxeViolations(dialog!);
    } finally {
      unmount();
      // Portal mounts on document.body — clean leftover backdrop.
      document.querySelector(".den-dialog-backdrop")?.remove();
      void container;
    }
  });

  it("Files info card chrome", async () => {
    const { FilesInfoCard } = await import("../files/components/FilesInfoCard.tsx");
    const { applyFilesBufferLoad, openFilesBuffer } = await import(
      "../files/documents/project-files-buffers.ts"
    );
    const key = openFilesBuffer("axe-p", {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "big.dat",
    });
    applyFilesBufferLoad("axe-p", key, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-axe",
      workspace_kind: "project",
path: "big.dat",
      content: "",
      over_limit: true,
      writable: true,
      binary: false,
      mime: "application/octet-stream",
      modified_at: "2026-01-01T00:00:00Z",
      size_bytes: 9_000_000,
    });
    const { projectFilesState } = await import(
      "../files/documents/files-buffer-state.ts"
    );
    const buf = projectFilesState("axe-p").byKey[key]!;
    const { container, unmount } = render(() => (
      <FilesInfoCard
        buffer={buf}
        roots={[
          {
            id: "r1",
            path: "/repo",
            label: "repo",
            is_primary: true,
            added_at: "2026-01-01T00:00:00Z",
            kind: "attached",
          },
        ]}
        onInnerLayerChange={() => undefined}
        onRevealSegment={() => undefined}
      />
    ));
    try {
      const root = container.querySelector(
        '[data-testid="files-info-card"]',
      ) as HTMLElement | null;
      expect(root).not.toBeNull();
      await assertNoAxeViolations(root!);
    } finally {
      unmount();
    }
  });

  it("Files image viewer chrome", async () => {
    const { FilesImageViewer } = await import(
      "../files/editor/FilesImageViewer.tsx"
    );
    const { applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests } =
      await import("../files/documents/project-files-buffers.ts");
    resetProjectFilesForTests();
    const key = openFilesBuffer("axe-img", {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.png",
    });
    applyFilesBufferLoad("axe-img", key, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-axe",
      workspace_kind: "project",
path: "a.png",
      content: "",
      over_limit: false,
      writable: true,
      binary: true,
      mime: "image/png",
      size_bytes: 120,
    });
    const { projectFilesState } = await import(
      "../files/documents/files-buffer-state.ts"
    );
    const buf = projectFilesState("axe-img").byKey[key]!;
    const { container, unmount } = render(() => (
      <FilesImageViewer
        projectId="axe-img"
        buffer={buf}
        roots={[
          {
            id: "r1",
            path: "/repo",
            label: "repo",
            is_primary: true,
            added_at: "2026-01-01T00:00:00Z",
            kind: "attached",
          },
        ]}
        client={null}
        onInnerLayerChange={() => undefined}
        onRevealSegment={() => undefined}
      />
    ));
    try {
      const root = container.querySelector(
        '[data-testid="files-image-viewer"]',
      ) as HTMLElement | null;
      expect(root).not.toBeNull();
      await assertNoAxeViolations(root!);
    } finally {
      unmount();
    }
  });

  it("Files open-files list chrome", async () => {
    const { FilesOpenList } = await import(
      "../files/tabs/FilesOpenList.tsx"
    );
    const {
      applyFilesBufferLoad,
      openFilesBuffer,
      resetProjectFilesForTests,
      setFilesBufferPinned,
    } = await import("../files/documents/project-files-buffers.ts");
    const { projectFilesState } = await import("../files/documents/files-buffer-state.ts");
    const { screen } = await import("@solidjs/testing-library");
    resetProjectFilesForTests();
    const a = openFilesBuffer("axe-list", {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.ts",
    });
    applyFilesBufferLoad("axe-list", a, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-axe",
      workspace_kind: "project",
path: "a.ts",
      content: "x",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 1,
      sha256: "s",
    });
    setFilesBufferPinned("axe-list", a, true);
    const b = openFilesBuffer("axe-list", {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "b.ts",
    });
    applyFilesBufferLoad("axe-list", b, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-axe",
      workspace_kind: "project",
path: "b.ts",
      content: "y",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 1,
      sha256: "t",
    });
    const state = projectFilesState("axe-list");
    const rows = state.order.map((key) => ({
      key,
      buffer: state.byKey[key]!,
    }));
    const { unmount } = render(() => (
      <FilesOpenList
        open
        rows={rows}
        activeKey={b}
        anchorEl={document.body}
        onClose={() => undefined}
        onActivate={() => undefined}
        onCloseBuffer={() => undefined}
        onRowContextMenu={() => undefined}
      />
    ));
    try {
      await assertNoAxeViolations(screen.getByTestId("files-open-list"));
    } finally {
      unmount();
    }
  });

  /** Each lens state gets its own accessibility scan. */
  describe("Review lens chrome", () => {
    const mountLens = async (files: unknown[]) => {
      const { ReviewLensHost: ReviewLens } = await import(
        "../files/review/review-lens-test-host.tsx"
      );
      const { resetFilesStagePaneForTests } = await import(
        "../files/review/review-pane.ts"
      );
      const { resetScopeResolutionForTests } = await import(
        "../files/tree/scope-resolution.ts"
      );
      const {
        reviewLensFixtureClient,
        reviewLensFixtureStore,
      } = await import("../files/review/review-lens-fixtures.ts");
      resetFilesStagePaneForTests();
      resetScopeResolutionForTests();
      const rendered = render(() => (
        <ReviewLens
          projectId="axe-changes"
          client={reviewLensFixtureClient(files as never)}
          appStore={reviewLensFixtureStore()}
          onOpenFile={() => undefined}
        />
      ));
      return rendered;
    };

    const lensFiles = async () => {
      const { reviewFileFixture, workingFileFixture } = await import(
        "../files/review/review-lens-fixtures.ts"
      );
      return [
        workingFileFixture("src/files/review/ReviewLens.tsx"),
        reviewFileFixture({ path: "src/files-domain.css" }),
      ];
    };

    it("empty scope", async () => {
      const { container, unmount } = await mountLens([]);
      try {
        const root = container.querySelector(
          '[data-testid="review-lens"]',
        ) as HTMLElement | null;
        expect(root).not.toBeNull();
        await assertNoAxeViolations(root!);
      } finally {
        unmount();
      }
    });

    it("populated list", async () => {
      const { container, unmount } = await mountLens(await lensFiles());
      try {
        await screen.findAllByTestId("changes-row-file");
        await assertNoAxeViolations(
          container.querySelector('[data-testid="review-lens"]') as HTMLElement,
        );
      } finally {
        unmount();
      }
    });

    it("expanded row detail with its step list", async () => {
      const { container, unmount } = await mountLens(await lensFiles());
      try {
        fireEvent.click((await screen.findAllByTestId("changes-row-file"))[0]!);
        const steps = await screen.findByTestId("changes-row-steps-toggle");
        fireEvent.click(steps);
        await screen.findAllByTestId("changes-step");
        await assertNoAxeViolations(
          container.querySelector('[data-testid="review-lens"]') as HTMLElement,
        );
      } finally {
        unmount();
      }
    });

  });
});
