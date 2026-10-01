/** Wire fixtures for API integration tests. */
import type {
  AttachmentCapabilities,
  McpCheckRow,
  McpProvider,
  PreflightReport,
  Session,
} from "../types.ts";
import { wireProject } from "./project-fixture.ts";

export const mockProjects = [wireProject("/tmp/demo")];

export const mockAttachmentCapabilities: AttachmentCapabilities = {
  auto_attach_paste_bytes: 16 * 1024,
  max_inline_text_bytes: 64 * 1024,
  max_attachments: 8,
  max_references: 64,
  max_images: 4,
  max_upload_bytes: 8 * 1024 * 1024,
  max_image_bytes: 8 * 1024 * 1024,
  max_body_bytes: 8 * 1024 * 1024,
  max_turn_bytes: 16 * 1024 * 1024,
  max_body_preview_bytes: 16 * 1024,
  max_large_text_preview_bytes: 8 * 1024,
  max_turn_preview_bytes: 32 * 1024,
  max_document_bytes: 8 * 1024 * 1024,
  image_mime_types: ["image/png"],
  max_video_bytes: 128 * 1024 * 1024,
  video_mime_types: ["video/mp4", "video/quicktime", "video/webm"],
  text_mime_types: ["text/plain"],
  text_extensions: [".txt"],
  text_basenames: [],
};

export function mockPreflightReport(
  overall: PreflightReport["overall"],
  probes: PreflightReport["probes"],
): PreflightReport {
  return {
    overall,
    probes,
    attachment_capabilities: mockAttachmentCapabilities,
  };
}

export const mockSession: Session = {
  id: "sess-1",
  owner_person_id: "00000000-0000-4000-8000-000000000002",
  project_id: "00000000-0000-4000-8000-000000000001",
  workspace_path: "/tmp/demo",
  posture: "build",
  status: "idle",
  created_at: "2025-01-01T00:00:00Z",
  activity_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
};

export const mockMcpProviders: McpProvider[] = [
  {
    id: "fixture",
    enabled: false,
    class: "local",
    tool_loading: "auto",
    transport: "http",
    status: "disabled",
    connection_source: "user",
  },
];

export const mockMcpCheck: McpCheckRow[] = [
  { provider_id: "fixture", status: "healthy" },
];
