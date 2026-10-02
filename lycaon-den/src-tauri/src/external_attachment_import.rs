//! Native external-file attachment snapshots.

use std::fs::{self, File};
use std::path::PathBuf;

use serde::{Deserialize, Serialize};

use crate::sidecar::{SidecarInfo, SidecarState};
use crate::sidecar::daemon::attach_existing_daemon;

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub struct ImportedAttachmentReceipt {
    pub blob_id: String,
    pub filename: String,
    pub mime: String,
    pub kind: String,
    pub bytes: i64,
    /// What the host's decoder read from a video; present only on video receipts.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub video: Option<ImportedVideoFacts>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub struct ImportedVideoFacts {
    pub duration_ms: f64,
    pub width: i64,
    pub height: i64,
}

#[derive(Debug, Clone, Serialize)]
pub struct AttachmentImportError {
    pub code: String,
    pub message: String,
}

impl AttachmentImportError {
    fn new(code: &str, message: impl Into<String>) -> Self {
        Self {
            code: code.to_string(),
            message: message.into(),
        }
    }
}

#[derive(Deserialize)]
struct SidecarErrorBody {
    code: String,
    message: String,
}

/// Stream a native drop into the active project's attachment store.
#[tauri::command]
pub async fn import_external_attachment(
    state: tauri::State<'_, SidecarState>,
    project_id: String,
    abs_path: String,
) -> Result<ImportedAttachmentReceipt, AttachmentImportError> {
    let sidecar = active_sidecar(&state)?;
    tauri::async_runtime::spawn_blocking(move || {
        import_external_attachment_at(sidecar, &project_id, &abs_path)
    })
    .await
    .map_err(|e| {
        AttachmentImportError::new(
            "attachment_unavailable",
            format!("attachment import did not complete: {e}"),
        )
    })?
}

fn active_sidecar(state: &SidecarState) -> Result<SidecarInfo, AttachmentImportError> {
    if let Some(info) = state.cached_info() {
        return Ok(info);
    }
    attach_existing_daemon()
        .map_err(|e| AttachmentImportError::new("attachment_unavailable", e))?
        .ok_or_else(|| {
            AttachmentImportError::new(
                "attachment_unavailable",
                "attachment import requires a running sidecar",
            )
        })
}

fn import_external_attachment_at(
    sidecar: SidecarInfo,
    project_id: &str,
    abs_path: &str,
) -> Result<ImportedAttachmentReceipt, AttachmentImportError> {
    let source = import_source(abs_path)
        .map_err(|e| AttachmentImportError::new("attachment_not_found", e))?;
    let filename = source_filename(abs_path)
        .map_err(|e| AttachmentImportError::new("attachment_not_found", e))?;
    let file = File::open(&source).map_err(|e| {
        AttachmentImportError::new("attachment_not_found", format!("open import source: {e}"))
    })?;
    let url = attachment_upload_url(&sidecar, project_id, &filename);
    let response = ureq::post(&url)
        .set("Authorization", &format!("Bearer {}", sidecar.api_token))
        .set("Content-Type", "application/octet-stream")
        .send(file);
    match response {
        Ok(response) => serde_json::from_reader(response.into_reader()).map_err(|e| {
            AttachmentImportError::new(
                "attachment_unavailable",
                format!("decode attachment receipt: {e}"),
            )
        }),
        Err(ureq::Error::Status(status, response)) => {
            let body = response.into_string().unwrap_or_default();
            Err(upload_response_error(status, &body))
        }
        Err(err) => Err(AttachmentImportError::new(
            "attachment_unavailable",
            format!("attachment upload failed: {err}"),
        )),
    }
}

fn upload_response_error(status: u16, body: &str) -> AttachmentImportError {
    match serde_json::from_str::<SidecarErrorBody>(body) {
        Ok(error) => AttachmentImportError::new(&error.code, error.message),
        Err(_) => AttachmentImportError::new(
            "attachment_unavailable",
            format!("attachment upload failed ({status})"),
        ),
    }
}

fn attachment_upload_url(sidecar: &SidecarInfo, project_id: &str, filename: &str) -> String {
    format!(
        "http://127.0.0.1:{}/v1/projects/{}/attachments?filename={}",
        sidecar.port,
        percent_encode(project_id.trim()),
        percent_encode(filename),
    )
}

fn import_source(abs_path: &str) -> Result<PathBuf, String> {
    let path = absolute_path(abs_path)?;
    let meta = fs::symlink_metadata(&path).map_err(|e| format!("inspect import source: {e}"))?;
    let target = if meta.is_symlink() {
        fs::canonicalize(&path).map_err(|e| format!("resolve import source: {e}"))?
    } else {
        path
    };
    if !fs::metadata(&target)
        .map_err(|e| format!("inspect import source: {e}"))?
        .is_file()
    {
        return Err("attachment import source is not a regular file".to_string());
    }
    Ok(target)
}

fn source_filename(abs_path: &str) -> Result<String, String> {
    absolute_path(abs_path)?
        .file_name()
        .and_then(|name| name.to_str())
        .filter(|name| !name.trim().is_empty())
        .map(str::to_owned)
        .ok_or_else(|| "attachment import source has no usable filename".to_string())
}

fn absolute_path(raw: &str) -> Result<PathBuf, String> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return Err("attachment import source path is required".to_string());
    }
    let path = PathBuf::from(trimmed);
    if !path.is_absolute() {
        return Err("attachment import source path must be absolute".to_string());
    }
    Ok(path)
}

fn percent_encode(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~') {
            out.push(byte as char);
        } else {
            out.push('%');
            out.push(hex(byte >> 4));
            out.push(hex(byte & 0x0f));
        }
    }
    out
}

fn hex(nibble: u8) -> char {
    match nibble {
        0..=9 => (b'0' + nibble) as char,
        _ => (b'A' + nibble - 10) as char,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    #[test]
    fn upload_errors_preserve_the_hosts_structured_reason() {
        for code in [
            "unauthorized",
            "project_not_found",
            "attachment_too_large",
            "internal_error",
        ] {
            let body =
                serde_json::json!({"code": code, "message": "Host explanation"}).to_string();
            for status in [400, 401, 404, 500] {
                let error = upload_response_error(status, &body);
                assert_eq!(error.code, code);
                assert_eq!(error.message, "Host explanation");
            }
        }
    }

    #[test]
    fn unreadable_error_responses_report_availability_without_inventing_a_file_rejection() {
        for status in [400, 500] {
            let error = upload_response_error(status, "not a host error envelope");
            assert_eq!(error.code, "attachment_unavailable");
            assert!(error.message.contains(&status.to_string()));
        }
    }

    #[test]
    fn import_source_resolves_a_file_but_refuses_a_folder() {
        let dir = TempDir::new("external-attachment");
        let file = dir.path().join("brief.txt");
        fs::write(&file, b"snapshot").expect("write import fixture");

        assert_eq!(
            import_source(&file.to_string_lossy()).expect("resolve file"),
            file
        );
        let err = import_source(&dir.path().to_string_lossy()).expect_err("folder must refuse");
        assert!(err.contains("regular file"), "unexpected error: {err}");
    }

    #[test]
    fn filename_and_url_encoding_never_preserve_a_source_path() {
        assert_eq!(
            source_filename("/outside/brief report.txt").expect("filename"),
            "brief report.txt"
        );
        assert_eq!(
            percent_encode("brief report.txt?x=1"),
            "brief%20report.txt%3Fx%3D1"
        );
        assert_eq!(percent_encode("project/a"), "project%2Fa");
        let url = attachment_upload_url(
            &SidecarInfo {
                port: 8123,
                api_token: "not-in-url".to_string(),
                generation: 1,
            },
            "project-1",
            "brief report.txt",
        );
        assert_eq!(
            url,
            "http://127.0.0.1:8123/v1/projects/project-1/attachments?filename=brief%20report.txt"
        );
        assert!(
            !url.contains("/outside/"),
            "source path leaked into upload URL"
        );
    }

    #[test]
    fn import_ipc_is_registered_and_permitted_only_as_a_snapshot_bridge() {
        let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let permissions = fs::read_to_string(manifest_dir.join("permissions/read-path.toml"))
            .expect("read attachment import permissions");
        let capability = fs::read_to_string(manifest_dir.join("capabilities/default.json"))
            .expect("read main-window capability");
        let app =
            fs::read_to_string(manifest_dir.join("src/lib.rs")).expect("read app registration");

        assert!(permissions.contains("allow-import-external-attachment"));
        assert!(capability.contains("allow-import-external-attachment"));
        assert!(app.contains("external_attachment_import::import_external_attachment"));
    }
}
