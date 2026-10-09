//! Streams archives between a picker-granted file and the authenticated local engine.

use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde::Serialize;
use sha2::{Digest, Sha256};
use tauri::{Emitter, Manager};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::Notify;

use crate::picked_file::{self, GrantMode};
use crate::sidecar::SidecarState;

fn archive_limit() -> u64 {
    static LIMIT: std::sync::LazyLock<u64> = std::sync::LazyLock::new(|| {
        let limits: serde_json::Value = serde_json::from_str(include_str!(
            "../../../lycaon/internal/backup/archive-limits.json"
        ))
        .expect("embedded backup limits");
        limits["compressed_bytes"]
            .as_u64()
            .filter(|value| *value > 0)
            .expect("positive archive limit")
    });
    *LIMIT
}

fn archive_limit_error() -> String {
    format!(
        "Choose a backup archive no larger than {} GiB",
        archive_limit() / (1 << 30)
    )
}
const BUFFER_BYTES: usize = 64 << 10;
const MAX_RESPONSE_BYTES: usize = 64 << 10;

#[derive(Default)]
pub struct BackupTransferState(Mutex<Option<Arc<Operation>>>);

struct ActiveTransfer<'a> {
    state: &'a BackupTransferState,
}

impl Drop for ActiveTransfer<'_> {
    fn drop(&mut self) {
        *self.state.0.lock().unwrap() = None;
    }
}

struct Operation {
    id: String,
    window_label: String,
    cancel: Notify,
    cancellable: Mutex<bool>,
}

#[derive(Clone, Serialize)]
pub struct TransferProgress {
    id: String,
    phase: &'static str,
    bytes: u64,
    total: Option<u64>,
    cancellable: bool,
}

fn progress(
    window: &tauri::WebviewWindow,
    op: &Operation,
    phase: &'static str,
    bytes: u64,
    total: Option<u64>,
) {
    let _ = window.emit(
        "backup-transfer-progress",
        TransferProgress {
            id: op.id.clone(),
            phase,
            bytes,
            total,
            cancellable: *op.cancellable.lock().unwrap(),
        },
    );
}

#[tauri::command]
pub fn cancel_backup_transfer(
    window: tauri::WebviewWindow,
    state: tauri::State<'_, BackupTransferState>,
    id: String,
) -> bool {
    let guard = state.0.lock().unwrap();
    let Some(op) = guard
        .as_ref()
        .filter(|op| op.id == id && op.window_label == window.label())
    else {
        return false;
    };
    if !*op.cancellable.lock().unwrap() {
        return false;
    }
    op.cancel.notify_one();
    true
}

#[tauri::command]
pub async fn transfer_backup(
    window: tauri::WebviewWindow,
    state: tauri::State<'_, BackupTransferState>,
    id: String,
    grant: String,
    restore: bool,
) -> Result<serde_json::Value, String> {
    if id.len() > 128 || id.is_empty() {
        return Err("Invalid backup operation".into());
    }
    let op = Arc::new(Operation {
        id,
        window_label: window.label().to_string(),
        cancel: Notify::new(),
        cancellable: Mutex::new(true),
    });
    {
        let mut active = state.0.lock().unwrap();
        if active.is_some() {
            return Err("Another backup or restore is running".into());
        }
        *active = Some(op.clone());
    }
    let _active = ActiveTransfer { state: &state };
    let work = transfer(&window, &op, grant, restore);
    tokio::pin!(work);
    let result = loop {
        tokio::select! {
            result = &mut work => break result,
            _ = op.cancel.notified() => {
                if *op.cancellable.lock().unwrap() { break Ok(serde_json::json!({"cancelled": true})); }
            },
        }
    };
    result
}

async fn transfer(
    window: &tauri::WebviewWindow,
    op: &Arc<Operation>,
    grant: String,
    restore: bool,
) -> Result<serde_json::Value, String> {
    let path = picked_file::redeem(
        &grant,
        if restore {
            GrantMode::Read
        } else {
            GrantMode::Write
        },
    )?;
    let info = crate::sidecar::commands::ipc_sidecar_info(
        window.app_handle().clone(),
        window.state::<SidecarState>(),
    )
    .await?
    .ok_or("The engine is unavailable")?;
    let client = transfer_client()?;
    let url = format!(
        "http://127.0.0.1:{}/v1/backup{}",
        info.port,
        if restore { "/restore" } else { "" }
    );
    if restore {
        upload(window, op, &client, &url, &info.api_token, &path).await
    } else {
        download(window, op, &client, &url, &info.api_token, &path).await
    }
}

fn transfer_client() -> Result<reqwest::Client, String> {
    let _ = rustls::crypto::ring::default_provider().install_default();
    reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(10))
        .build()
        .map_err(|e| e.to_string())
}

async fn response_json(mut response: reqwest::Response) -> Result<serde_json::Value, String> {
    let success = response.status().is_success();
    let mut bytes = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|e| e.to_string())? {
        if bytes.len() + chunk.len() > MAX_RESPONSE_BYTES {
            return Err("Engine response exceeds its limit".into());
        }
        bytes.extend_from_slice(&chunk);
    }
    let value: serde_json::Value =
        serde_json::from_slice(&bytes).map_err(|_| "Invalid engine response")?;
    if !success {
        return Err(value
            .get("message")
            .and_then(|v| v.as_str())
            .unwrap_or("Backup operation failed")
            .to_string());
    }
    Ok(value)
}

async fn upload(
    window: &tauri::WebviewWindow,
    op: &Arc<Operation>,
    client: &reqwest::Client,
    url: &str,
    token: &str,
    path: &Path,
) -> Result<serde_json::Value, String> {
    let file = tokio::fs::File::open(path)
        .await
        .map_err(|e| e.to_string())?;
    let metadata = file.metadata().await.map_err(|e| e.to_string())?;
    let total = metadata.len();
    if !metadata.is_file() || total == 0 || total > archive_limit() {
        return Err(archive_limit_error());
    }
    progress(window, op, "uploading", 0, Some(total));
    let window_copy = window.clone();
    let op_copy = op.clone();
    let stream = futures_util::stream::try_unfold(
        (file, 0u64, Instant::now()),
        move |(mut file, sent, mut last)| {
            let window = window_copy.clone();
            let op = op_copy.clone();
            async move {
                if sent == total {
                    return Ok::<_, std::io::Error>(None);
                }
                let mut buffer = vec![0; BUFFER_BYTES.min((total - sent) as usize)];
                let n = file.read(&mut buffer).await?;
                if n == 0 {
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::UnexpectedEof,
                        "Backup file changed during upload",
                    ));
                }
                buffer.truncate(n);
                let sent = sent + n as u64;
                if sent == total {
                    *op.cancellable.lock().unwrap() = false;
                    progress(&window, &op, "validating", sent, Some(total));
                } else if last.elapsed() >= Duration::from_millis(150) {
                    progress(&window, &op, "uploading", sent, Some(total));
                    last = Instant::now();
                }
                Ok(Some((buffer, (file, sent, last))))
            }
        },
    );
    let response = client
        .post(url)
        .bearer_auth(token)
        .header("Content-Type", "application/zip")
        .header("Content-Length", total)
        .body(reqwest::Body::wrap_stream(stream))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    response_json(response).await
}

async fn download(
    window: &tauri::WebviewWindow,
    op: &Operation,
    client: &reqwest::Client,
    url: &str,
    token: &str,
    path: &Path,
) -> Result<serde_json::Value, String> {
    progress(window, op, "preparing", 0, None);
    let response = client
        .get(url)
        .bearer_auth(token)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !response.status().is_success() {
        return response_json(response).await;
    }
    let config = crate::config_dir::host_config_dir()?;
    receive_archive(response, path, &config, |phase, bytes, total| {
        if phase == "saving" {
            *op.cancellable.lock().unwrap() = false;
        }
        progress(window, op, phase, bytes, total);
    })
    .await
}

async fn receive_archive(
    mut response: reqwest::Response,
    path: &Path,
    config: &Path,
    mut report: impl FnMut(&'static str, u64, Option<u64>),
) -> Result<serde_json::Value, String> {
    let total = response.content_length();
    if total.is_some_and(|n| n > archive_limit()) {
        return Err(archive_limit_error());
    }
    let expected_digest = response
        .headers()
        .get("X-Backup-SHA256")
        .and_then(|value| value.to_str().ok())
        .filter(|value| value.len() == 64 && value.bytes().all(|b| b.is_ascii_hexdigit()))
        .ok_or("The engine did not provide a backup integrity digest")?
        .to_ascii_lowercase();
    let mut digest = Sha256::new();
    let parent = path.parent().ok_or("Invalid backup destination")?;
    let (temporary, opened) =
        crate::backup_transfer_journal::TemporaryArchive::create(config, parent)
            .map_err(|error| error.to_string())?;
    let mut file = tokio::fs::File::from_std(opened);
    let mut received = 0;
    let mut last = Instant::now();
    while let Some(chunk) = response.chunk().await.map_err(|e| e.to_string())? {
        received += chunk.len() as u64;
        if received > archive_limit() {
            return Err(archive_limit_error());
        }
        file.write_all(&chunk).await.map_err(|e| e.to_string())?;
        digest.update(&chunk);
        if last.elapsed() >= Duration::from_millis(150) {
            report("downloading", received, total);
            last = Instant::now();
        }
    }
    if total.is_some_and(|n| n != received) {
        return Err("Backup download was incomplete".into());
    }
    if hex::encode(digest.finalize()) != expected_digest {
        return Err("Backup download failed its integrity check".into());
    }
    file.sync_all().await.map_err(|e| e.to_string())?;
    drop(file);
    report("saving", received, total);
    crate::atomic_file::replace(temporary.path(), path, true).map_err(|e| e.to_string())?;
    Ok(serde_json::json!({"saved": true}))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};

    #[tokio::test]
    async fn reports_the_engine_error_envelope_message() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("error listener");
        let address = listener.local_addr().expect("error address");
        let sender = std::thread::spawn(move || {
            let (mut socket, _) = listener.accept().expect("error connection");
            let mut request = [0u8; 4096];
            socket.read(&mut request).expect("error request");
            let body = br#"{"code":"backup_incompatible","message":"Archive schema does not match this app."}"#;
            write!(socket, "HTTP/1.1 409 Conflict\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).expect("error headers");
            socket.write_all(body).expect("error body");
        });
        let response = transfer_client()
            .expect("error client")
            .get(format!("http://{address}/restore"))
            .send()
            .await
            .expect("error response");
        assert_eq!(
            response_json(response).await.expect_err("restore refused"),
            "Archive schema does not match this app."
        );
        sender.join().expect("error sender");
    }

    #[tokio::test]
    async fn generated_large_transfer_has_bounded_buffers_and_verified_bytes() {
        let length = std::env::var("PW_BACKUP_TRANSFER_STRESS_BYTES")
            .map(|value| value.parse::<u64>().expect("stress byte count"))
            .unwrap_or(8 << 20);
        assert!(length > 0 && length <= archive_limit());
        let dir = crate::test_support::TempDir::new("backup-scale");
        let target = dir.join("backup.zip");
        std::fs::write(&target, b"previous").expect("previous backup");
        let mut block = vec![0u8; BUFFER_BYTES];
        let mut state = 0xa17d_8f20_d497_730bu64;
        for byte in &mut block {
            state ^= state << 13;
            state ^= state >> 7;
            state ^= state << 17;
            *byte = state as u8;
        }
        let mut hash = Sha256::new();
        let mut remaining = length;
        while remaining > 0 {
            let count = remaining.min(block.len() as u64) as usize;
            hash.update(&block[..count]);
            remaining -= count as u64;
        }
        let expected = hex::encode(hash.finalize());
        let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("scale listener");
        let address = listener.local_addr().expect("scale address");
        let digest_header = expected.clone();
        let sender = std::thread::spawn(move || {
            let (mut socket, _) = listener.accept().expect("scale accept");
            let mut request = [0u8; 4096];
            socket.read(&mut request).expect("scale request");
            write!(socket, "HTTP/1.1 200 OK\r\nContent-Length: {length}\r\nX-Backup-SHA256: {digest_header}\r\nConnection: close\r\n\r\n").expect("scale headers");
            let mut remaining = length;
            while remaining > 0 {
                let count = remaining.min(block.len() as u64) as usize;
                socket.write_all(&block[..count]).expect("scale body");
                remaining -= count as u64;
            }
        });
        let started = Instant::now();
        let response = transfer_client()
            .expect("scale client")
            .get(format!("http://{address}/archive"))
            .send()
            .await
            .expect("scale response");
        receive_archive(response, &target, &dir, |phase, count, _| {
            if phase == "saving" {
                assert_eq!(count, length);
                assert_eq!(
                    std::fs::read(&target).expect("old backup before commit"),
                    b"previous"
                );
            }
        })
        .await
        .expect("scale save");
        sender.join().expect("scale sender");
        assert_eq!(
            std::fs::metadata(&target).expect("saved metadata").len(),
            length
        );
        let mut file = std::fs::File::open(&target).expect("saved archive");
        let mut digest = Sha256::new();
        let mut buffer = [0u8; BUFFER_BYTES];
        loop {
            let count = file.read(&mut buffer).expect("verify bytes");
            if count == 0 {
                break;
            }
            digest.update(&buffer[..count]);
        }
        assert_eq!(hex::encode(digest.finalize()), expected);
        assert_eq!(
            std::fs::read_dir(&dir).expect("no temporary files").count(),
            2
        );
        if let Ok(report) = std::env::var("PW_BACKUP_TRANSFER_REPORT") {
            let mut usage: libc::rusage = unsafe { std::mem::zeroed() };
            assert_eq!(unsafe { libc::getrusage(libc::RUSAGE_SELF, &mut usage) }, 0);
            let peak_bytes = if cfg!(target_os = "macos") {
                usage.ru_maxrss as u64
            } else {
                usage.ru_maxrss as u64 * 1024
            };
            std::fs::write(report, serde_json::to_vec_pretty(&serde_json::json!({
                "bytes": length, "seconds_including_readback": started.elapsed().as_secs_f64(),
                "process_peak_resident_bytes": peak_bytes, "fixture_buffer_bytes": BUFFER_BYTES,
                "sha256": expected, "integrity_verified": true, "old_destination_preserved_until_commit": true
            })).expect("serialize report")).expect("write measurement");
            if length >= 1 << 30 {
                assert!(
                    peak_bytes < 512 << 20,
                    "large transfer used {peak_bytes} resident bytes"
                );
            }
        }
    }

    fn server(length: u64, body: Vec<u8>, delay: Duration) -> String {
        let digest = hex::encode(Sha256::digest(&body));
        server_with_digest(length, body, delay, digest)
    }

    fn server_with_digest(length: u64, body: Vec<u8>, delay: Duration, digest: String) -> String {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind fixture");
        let address = listener.local_addr().expect("fixture address");
        std::thread::spawn(move || {
            let (mut socket, _) = listener.accept().expect("accept fixture");
            let mut request = [0u8; 4096];
            let _ = socket.read(&mut request);
            let _ = write!(
                socket,
                "HTTP/1.1 200 OK\r\nContent-Length: {length}\r\nX-Backup-SHA256: {digest}\r\nConnection: close\r\n\r\n"
            );
            std::thread::sleep(delay);
            for chunk in body.chunks(BUFFER_BYTES) {
                if socket.write_all(chunk).is_err() {
                    break;
                }
            }
        });
        format!("http://{address}/archive")
    }

    async fn response(length: u64, body: Vec<u8>, delay: Duration) -> reqwest::Response {
        transfer_client()
            .expect("fixture client")
            .get(server(length, body, delay))
            .send()
            .await
            .expect("fixture response")
    }

    #[tokio::test]
    async fn streamed_archive_replaces_destination_only_when_complete() {
        let dir = crate::test_support::TempDir::new("backup-stream");
        let target = dir.join("backup.zip");
        std::fs::write(&target, b"previous").expect("seed previous");
        let body = vec![0x5a; 4 << 20];
        let response = response(body.len() as u64, body.clone(), Duration::ZERO).await;
        let mut saved = false;
        receive_archive(response, &target, &dir, |phase, count, total| {
            if phase == "saving" {
                assert_eq!(count, body.len() as u64);
                assert_eq!(total, Some(count));
                assert_eq!(
                    std::fs::read(&target).expect("prior still present"),
                    b"previous"
                );
                saved = true;
            }
        })
        .await
        .expect("save archive");
        assert!(saved);
        assert_eq!(std::fs::read(&target).expect("complete archive"), body);
        assert_eq!(std::fs::read_dir(&dir).expect("directory").count(), 2);
    }

    #[tokio::test]
    async fn interrupted_download_keeps_previous_file() {
        let dir = crate::test_support::TempDir::new("backup-truncated");
        let target = dir.join("backup.zip");
        std::fs::write(&target, b"previous").expect("seed previous");
        let response = response(1024, vec![0; 17], Duration::ZERO).await;
        receive_archive(response, &target, &dir, |_, _, _| {})
            .await
            .expect_err("truncation refused");
        assert_eq!(std::fs::read(&target).expect("prior intact"), b"previous");
        assert_eq!(std::fs::read_dir(&dir).expect("directory").count(), 2);
    }

    #[tokio::test]
    async fn corrupt_complete_download_keeps_previous_file() {
        let dir = crate::test_support::TempDir::new("backup-integrity");
        let target = dir.join("backup.zip");
        std::fs::write(&target, b"previous").expect("seed previous");
        let response = transfer_client()
            .expect("integrity client")
            .get(server_with_digest(
                17,
                vec![1; 17],
                Duration::ZERO,
                "0".repeat(64),
            ))
            .send()
            .await
            .expect("integrity response");
        assert!(receive_archive(response, &target, &dir, |_, _, _| {})
            .await
            .expect_err("digest mismatch")
            .contains("integrity"));
        assert_eq!(std::fs::read(&target).expect("prior intact"), b"previous");
        assert_eq!(std::fs::read_dir(&dir).expect("directory").count(), 2);
    }

    #[tokio::test]
    async fn cancelled_download_removes_partial_file() {
        let dir = crate::test_support::TempDir::new("backup-cancelled");
        let target = dir.join("backup.zip");
        std::fs::write(&target, b"previous").expect("seed previous");
        let response = response(1024, vec![0; 1024], Duration::from_millis(200)).await;
        tokio::select! {
            result = receive_archive(response, &target, &dir, |_, _, _| {}) => panic!("completed before cancellation: {result:?}"),
            _ = tokio::time::sleep(Duration::from_millis(20)) => {},
        }
        assert_eq!(std::fs::read(&target).expect("prior intact"), b"previous");
        assert_eq!(std::fs::read_dir(&dir).expect("directory").count(), 2);
    }

    #[tokio::test]
    async fn oversized_archive_is_rejected_before_creating_a_file() {
        let dir = crate::test_support::TempDir::new("backup-size");
        let response = response(archive_limit() + 1, vec![], Duration::ZERO).await;
        receive_archive(response, &dir.join("backup.zip"), &dir, |_, _, _| {})
            .await
            .expect_err("size cap");
        assert_eq!(std::fs::read_dir(&dir).expect("directory").count(), 0);
    }
}
