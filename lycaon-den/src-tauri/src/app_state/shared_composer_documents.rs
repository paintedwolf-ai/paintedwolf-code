//! Pending composer state shared by workspace views.

use std::collections::HashMap;
use std::sync::{LazyLock, Mutex};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use tauri::{AppHandle, Emitter};

const MAX_ATTACHMENTS: usize = 128;
// Bounds draft IPC allocation above the prompt policy limit.
const MAX_DRAFT_TRANSPORT_BYTES: usize = 4 * 1024 * 1024;
const MAX_ATTACHMENTS_JSON_BYTES: usize = 64 * 1024 * 1024;

#[derive(Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ComposerDocumentSeed {
    pub project_id: String,
    pub session_id: String,
    pub draft: String,
    pub attachments: Vec<ComposerAttachment>,
}

#[derive(Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ComposerAttachment {
    pub id: String,
    pub kind: ComposerAttachmentKind,
    pub name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub project_id: Option<String>,
    #[serde(flatten)]
    pub fields: Map<String, Value>,
}

#[derive(Clone, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "kebab-case")]
pub enum ComposerAttachmentKind {
    Image,
    Text,
    Document,
    Video,
    Reject,
    PathFile,
    PathFolder,
    Artifact,
    SearchHit,
    Secret,
}

impl ComposerAttachmentKind {
    fn is_project_reference(&self) -> bool {
        matches!(
            self,
            Self::PathFile | Self::PathFolder | Self::Artifact | Self::SearchHit | Self::Secret
        )
    }
}

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SharedComposerDocument {
    pub project_id: String,
    pub session_id: String,
    pub draft: String,
    pub attachments: Vec<ComposerAttachment>,
    pub revision: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub lease_client_id: Option<String>,
}

static DOCUMENTS: LazyLock<Mutex<HashMap<String, SharedComposerDocument>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

fn valid_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 256
        && value
            .chars()
            .all(|ch| ch.is_ascii_alphanumeric() || matches!(ch, '-' | '_' | ':'))
}

fn validate_seed(seed: &ComposerDocumentSeed) -> Result<(), String> {
    if !valid_id(&seed.project_id) || !valid_id(&seed.session_id) {
        return Err("invalid shared composer document identity".into());
    }
    validate_attachments(&seed.project_id, &seed.attachments)?;
    validate_content(&seed.draft, &seed.attachments)
}

fn validate_attachments(
    project_id: &str,
    attachments: &[ComposerAttachment],
) -> Result<(), String> {
    for attachment in attachments {
        if !valid_id(&attachment.id)
            || attachment.name.trim().is_empty()
            || attachment.name.len() > 4096
        {
            return Err("invalid shared composer attachment".into());
        }
        if attachment.kind.is_project_reference()
            && attachment.project_id.as_deref() != Some(project_id)
        {
            return Err("shared composer reference belongs to another project".into());
        }
        if attachment.kind == ComposerAttachmentKind::Secret {
            validate_secret_attachment(attachment)?;
        }
    }
    Ok(())
}

fn validate_secret_attachment(attachment: &ComposerAttachment) -> Result<(), String> {
    const ALLOWED_FIELDS: [&str; 4] = ["reference", "scope", "shape", "runeLength"];
    if attachment
        .fields
        .keys()
        .any(|key| !ALLOWED_FIELDS.contains(&key.as_str()))
    {
        return Err("secret attachment contains an unsupported field".into());
    }
    let reference = attachment
        .fields
        .get("reference")
        .and_then(Value::as_str)
        .ok_or_else(|| "secret attachment reference is required".to_string())?;
    let Some(secret_id) = reference
        .strip_prefix("{{paintedwolf-secret:")
        .and_then(|value| value.strip_suffix("}}"))
    else {
        return Err("secret attachment reference is invalid".into());
    };
    let parsed = uuid::Uuid::parse_str(secret_id)
        .map_err(|_| "secret attachment reference is invalid".to_string())?;
    if parsed.to_string() != secret_id {
        return Err("secret attachment reference is invalid".into());
    }
    match attachment.fields.get("scope").and_then(Value::as_str) {
        Some("chat" | "project") => {}
        _ => return Err("secret attachment scope is invalid".into()),
    }
    match attachment.fields.get("runeLength").and_then(Value::as_u64) {
        Some(1..=4_194_304) => {}
        _ => return Err("secret attachment length is invalid".into()),
    }
    if attachment
        .fields
        .get("shape")
        .is_some_and(|value| value.as_str().is_none_or(|shape| shape.len() > 256))
    {
        return Err("secret attachment shape is invalid".into());
    }
    Ok(())
}

fn validate_content(draft: &str, attachments: &[ComposerAttachment]) -> Result<(), String> {
    if draft.len() > MAX_DRAFT_TRANSPORT_BYTES || attachments.len() > MAX_ATTACHMENTS {
        return Err("shared composer document exceeds its size limit".into());
    }
    let encoded = serde_json::to_vec(attachments).map_err(|err| err.to_string())?;
    if encoded.len() > MAX_ATTACHMENTS_JSON_BYTES {
        return Err("shared composer attachments exceed their size limit".into());
    }
    Ok(())
}

fn document_key(project_id: &str, session_id: &str) -> Result<String, String> {
    if !valid_id(project_id) || !valid_id(session_id) {
        return Err("invalid shared composer document identity".into());
    }
    Ok(format!("{project_id}\0{session_id}"))
}

fn record_from_seed(seed: ComposerDocumentSeed) -> SharedComposerDocument {
    SharedComposerDocument {
        project_id: seed.project_id,
        session_id: seed.session_id,
        draft: seed.draft,
        attachments: seed.attachments,
        revision: 1,
        lease_client_id: None,
    }
}

fn emit_document(app: &AppHandle, document: &SharedComposerDocument) {
    let _ = app.emit("shared-composer-document-changed", document);
}

#[tauri::command(rename = "resolve_shared_composer_document")]
pub fn resolve_shared_composer_document(
    seed: ComposerDocumentSeed,
) -> Result<SharedComposerDocument, String> {
    validate_seed(&seed)?;
    let key = document_key(&seed.project_id, &seed.session_id)?;
    let mut documents = DOCUMENTS
        .lock()
        .map_err(|_| "shared composer document lock poisoned".to_string())?;
    Ok(documents
        .entry(key)
        .or_insert_with(|| record_from_seed(seed))
        .clone())
}

#[tauri::command(rename = "acquire_shared_composer_document_lease")]
pub fn acquire_shared_composer_document_lease(
    app: AppHandle,
    seed: ComposerDocumentSeed,
    view_id: String,
) -> Result<SharedComposerDocument, String> {
    validate_seed(&seed)?;
    if !valid_id(&view_id) {
        return Err("invalid shared composer editor identity".into());
    }
    let key = document_key(&seed.project_id, &seed.session_id)?;
    let document = {
        let mut documents = DOCUMENTS
            .lock()
            .map_err(|_| "shared composer document lock poisoned".to_string())?;
        let document = documents
            .entry(key)
            .or_insert_with(|| record_from_seed(seed));
        if document.lease_client_id.as_deref() != Some(view_id.as_str()) {
            document.lease_client_id = Some(view_id);
            document.revision = document.revision.saturating_add(1);
        }
        document.clone()
    };
    emit_document(&app, &document);
    Ok(document)
}

#[tauri::command(rename = "update_shared_composer_document_draft")]
pub fn update_shared_composer_document_draft(
    app: AppHandle,
    project_id: String,
    session_id: String,
    view_id: String,
    revision: u64,
    draft: String,
) -> Result<SharedComposerDocument, String> {
    if !valid_id(&view_id) || draft.len() > MAX_DRAFT_TRANSPORT_BYTES {
        return Err("invalid shared composer draft update".into());
    }
    let key = document_key(&project_id, &session_id)?;
    let document = {
        let mut documents = DOCUMENTS
            .lock()
            .map_err(|_| "shared composer document lock poisoned".to_string())?;
        let Some(document) = documents.get_mut(&key) else {
            return Err("shared composer document was not resolved".into());
        };
        if document.lease_client_id.as_deref() != Some(view_id.as_str()) {
            return Err("shared composer draft is being edited in another window".into());
        }
        if document.revision != revision {
            return Err("shared composer document changed before this edit was applied".into());
        }
        document.draft = draft;
        document.revision = document.revision.saturating_add(1);
        document.clone()
    };
    emit_document(&app, &document);
    Ok(document)
}

#[tauri::command(rename = "apply_shared_composer_document_mutation")]
pub fn apply_shared_composer_document_mutation(
    app: AppHandle,
    seed: ComposerDocumentSeed,
    attachments: Vec<ComposerAttachment>,
    draft_prefill: Option<String>,
) -> Result<SharedComposerDocument, String> {
    validate_seed(&seed)?;
    validate_attachments(&seed.project_id, &attachments)?;
    if draft_prefill
        .as_ref()
        .is_some_and(|draft| draft.len() > MAX_DRAFT_TRANSPORT_BYTES)
    {
        return Err("shared composer draft prefill exceeds its size limit".into());
    }
    let key = document_key(&seed.project_id, &seed.session_id)?;
    let document = {
        let mut documents = DOCUMENTS
            .lock()
            .map_err(|_| "shared composer document lock poisoned".to_string())?;
        let document = documents
            .entry(key)
            .or_insert_with(|| record_from_seed(seed));
        let mut merged = document.attachments.clone();
        for attachment in attachments {
            if merged.iter().any(|existing| existing.id == attachment.id) {
                continue;
            }
            merged.push(attachment);
        }
        validate_content(&document.draft, &merged)?;
        document.attachments = merged;
        if document.draft.trim().is_empty() {
            if let Some(prefill) = draft_prefill.filter(|draft| !draft.is_empty()) {
                document.draft = prefill;
            }
        }
        document.revision = document.revision.saturating_add(1);
        document.clone()
    };
    emit_document(&app, &document);
    Ok(document)
}

#[tauri::command(rename = "protect_shared_composer_selection")]
pub fn protect_shared_composer_selection(
    app: AppHandle,
    project_id: String,
    session_id: String,
    view_id: String,
    revision: u64,
    draft: String,
    attachment: ComposerAttachment,
) -> Result<SharedComposerDocument, String> {
    if !valid_id(&view_id) || draft.len() > MAX_DRAFT_TRANSPORT_BYTES {
        return Err("invalid protected composer update".into());
    }
    if attachment.kind != ComposerAttachmentKind::Secret {
        return Err("protected composer update requires a secret attachment".into());
    }
    validate_attachments(&project_id, std::slice::from_ref(&attachment))?;
    let key = document_key(&project_id, &session_id)?;
    let document = {
        let mut documents = DOCUMENTS
            .lock()
            .map_err(|_| "shared composer document lock poisoned".to_string())?;
        let Some(document) = documents.get_mut(&key) else {
            return Err("shared composer document was not resolved".into());
        };
        if document.lease_client_id.as_deref() != Some(view_id.as_str()) {
            return Err("shared composer draft is being edited in another window".into());
        }
        if document.revision != revision {
            return Err("shared composer document changed before this secret was protected".into());
        }
        let mut merged = document.attachments.clone();
        if !merged.iter().any(|existing| existing.id == attachment.id) {
            merged.push(attachment);
        }
        validate_content(&draft, &merged)?;
        document.draft = draft;
        document.attachments = merged;
        document.revision = document.revision.saturating_add(1);
        document.clone()
    };
    emit_document(&app, &document);
    Ok(document)
}

#[tauri::command(rename = "remove_shared_composer_document_attachment")]
pub fn remove_shared_composer_document_attachment(
    app: AppHandle,
    project_id: String,
    session_id: String,
    attachment_id: String,
) -> Result<SharedComposerDocument, String> {
    if !valid_id(&attachment_id) {
        return Err("invalid shared composer attachment identity".into());
    }
    let key = document_key(&project_id, &session_id)?;
    let document = {
        let mut documents = DOCUMENTS
            .lock()
            .map_err(|_| "shared composer document lock poisoned".to_string())?;
        let Some(document) = documents.get_mut(&key) else {
            return Err("shared composer document was not resolved".into());
        };
        document
            .attachments
            .retain(|attachment| attachment.id != attachment_id);
        document.revision = document.revision.saturating_add(1);
        document.clone()
    };
    emit_document(&app, &document);
    Ok(document)
}

#[tauri::command(rename = "clear_shared_composer_document")]
pub fn clear_shared_composer_document(
    app: AppHandle,
    project_id: String,
    session_id: String,
    clear_draft: bool,
) -> Result<SharedComposerDocument, String> {
    let key = document_key(&project_id, &session_id)?;
    let document = {
        let mut documents = DOCUMENTS
            .lock()
            .map_err(|_| "shared composer document lock poisoned".to_string())?;
        let Some(document) = documents.get_mut(&key) else {
            return Err("shared composer document was not resolved".into());
        };
        document.attachments.clear();
        if clear_draft {
            document.draft.clear();
        }
        document.revision = document.revision.saturating_add(1);
        document.clone()
    };
    emit_document(&app, &document);
    Ok(document)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn seed(session_id: &str) -> ComposerDocumentSeed {
        ComposerDocumentSeed {
            project_id: "project-1".into(),
            session_id: session_id.into(),
            draft: String::new(),
            attachments: Vec::new(),
        }
    }

    fn attachment(value: Value) -> ComposerAttachment {
        serde_json::from_value(value).unwrap()
    }

    #[test]
    fn identity_is_project_and_session_scoped() {
        assert_ne!(
            document_key("project-1", "session-1").unwrap(),
            document_key("project-2", "session-1").unwrap()
        );
        assert_ne!(
            document_key("project-1", "session-1").unwrap(),
            document_key("project-1", "session-2").unwrap()
        );
    }

    #[test]
    fn seed_validation_rejects_placeholder_destinations() {
        assert!(validate_seed(&ComposerDocumentSeed {
            session_id: "home/unknown".into(),
            ..seed("session-1")
        })
        .is_err());
    }

    #[test]
    fn attachment_payload_is_bounded() {
        let oversized = (0..=MAX_ATTACHMENTS)
            .map(|index| {
                attachment(serde_json::json!({
                    "id": format!("attachment-{index}"),
                    "kind": "text",
                    "name": "note.txt",
                    "mime": "text/plain",
                    "byteLength": 1
                }))
            })
            .collect::<Vec<_>>();
        assert!(validate_content("", &oversized).is_err());
    }

    #[test]
    fn cross_project_reference_is_rejected() {
        let attachment = attachment(serde_json::json!({
            "id": "attachment-1",
            "kind": "artifact",
            "name": "Result",
            "projectId": "project-2",
            "artifactId": "artifact-1"
        }));
        assert!(validate_attachments("project-1", &[attachment]).is_err());
    }

    #[test]
    fn video_attachment_round_trips_with_its_facts() {
        let value = serde_json::json!({
            "id": "attachment-1",
            "kind": "video",
            "name": "bug.mp4",
            "blobId": "blob-1",
            "mime": "video/mp4",
            "byteLength": 1024,
            "video": {"duration_ms": 3000, "width": 320, "height": 180}
        });
        let parsed = attachment(value.clone());
        assert!(parsed.kind == ComposerAttachmentKind::Video);
        assert_eq!(serde_json::to_value(&parsed).unwrap(), value);
    }

    #[test]
    fn unknown_attachment_kind_is_rejected_by_the_wire_shape() {
        assert!(
            serde_json::from_value::<ComposerAttachment>(serde_json::json!({
                "id": "attachment-1",
                "kind": "unknown",
                "name": "note.txt"
            }))
            .is_err()
        );
    }

    #[test]
    fn secret_attachment_is_value_free_and_typed() {
        let valid = attachment(serde_json::json!({
            "id": "attachment-1",
            "kind": "secret",
            "name": "API token",
            "projectId": "project-1",
            "reference": "{{paintedwolf-secret:123e4567-e89b-12d3-a456-426614174000}}",
            "scope": "chat",
            "shape": "1 character",
            "runeLength": 1
        }));
        assert!(validate_attachments("project-1", &[valid]).is_ok());

        let plaintext = attachment(serde_json::json!({
            "id": "attachment-2",
            "kind": "secret",
            "name": "API token",
            "projectId": "project-1",
            "reference": "{{paintedwolf-secret:123e4567-e89b-12d3-a456-426614174000}}",
            "scope": "chat",
            "runeLength": 1,
            "secretValue": "x"
        }));
        assert!(validate_attachments("project-1", &[plaintext]).is_err());
    }
    #[test]
    fn secret_attachment_requires_a_namespaced_canonical_uuid() {
        for reference in [
            "{{secret:123e4567-e89b-12d3-a456-426614174000}}",
            "{{paintedwolf-secret:123E4567-e89b-12d3-a456-426614174000}}",
            "{{paintedwolf-secret:123e4567e89b12d3a456426614174000}}",
            "{{paintedwolf-secret:example}}",
            "{{paintedwolf-secret:",
        ] {
            let candidate = attachment(serde_json::json!({
                "id": "attachment-1",
                "kind": "secret",
                "name": "API token",
                "projectId": "project-1",
                "reference": reference,
                "scope": "chat",
                "shape": "4 characters",
                "runeLength": 4
            }));
            assert!(
                validate_attachments("project-1", &[candidate]).is_err(),
                "{reference}"
            );
        }
    }
}
