//! One bounded private request resolves policy for one effective destination.
use serde::{Deserialize, Serialize};
use std::process::Stdio;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

const OUTPUT_LIMIT: u64 = 64 * 1024;
const HELPER_TIMEOUT: Duration = Duration::from_secs(6);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum ProxyError {
    RestartRequired(crate::sidecar::captured_engine::AdmissionError),
    HelperUnavailable,
    InvalidReply,
    AutomaticUnsupported,
    ResolutionFailed,
    EndpointUnsupported,
}

impl std::fmt::Display for ProxyError {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output.write_str(match self {
            Self::RestartRequired(reason) => reason.guidance(),
            Self::HelperUnavailable => "The update proxy helper is unavailable or did not respond in time",
            Self::InvalidReply => "The update proxy helper returned an invalid result",
            Self::AutomaticUnsupported => "Automatic proxy configuration is unsupported; configure a manual proxy to check for updates",
            Self::ResolutionFailed => "The configured proxy policy could not be read; check the system or environment proxy settings",
            Self::EndpointUnsupported => "The configured proxy endpoint is unsupported",
        })
    }
}

#[derive(Serialize)]
struct Request<'a> {
    format_version: u32,
    target: &'a str,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Reply {
    format_version: u32,
    target: String,
    proxy_url: serde_json::Value,
    #[serde(default)]
    error_code: Option<String>,
}

pub(super) async fn resolve(target: &str) -> Result<Option<String>, ProxyError> {
    let command =
        crate::sidecar::captured_engine::command().map_err(ProxyError::RestartRequired)?;
    exchange(command, target).await
}

async fn exchange(
    mut command: tokio::process::Command,
    target: &str,
) -> Result<Option<String>, ProxyError> {
    let requested = serde_json::to_vec(&Request {
        format_version: 1,
        target,
    })
    .map_err(|_| ProxyError::InvalidReply)?;
    let mut child = command
        .arg("internal-system-proxy")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .kill_on_drop(true)
        .spawn()
        .map_err(|_| ProxyError::HelperUnavailable)?;
    let result = tokio::time::timeout(HELPER_TIMEOUT, async {
        let mut input = child.stdin.take().ok_or(ProxyError::HelperUnavailable)?;
        input
            .write_all(&requested)
            .await
            .map_err(|_| ProxyError::HelperUnavailable)?;
        drop(input);
        let output = child.stdout.take().ok_or(ProxyError::HelperUnavailable)?;
        let mut raw = Vec::new();
        output
            .take(OUTPUT_LIMIT + 1)
            .read_to_end(&mut raw)
            .await
            .map_err(|_| ProxyError::HelperUnavailable)?;
        if raw.len() as u64 > OUTPUT_LIMIT {
            return Err(ProxyError::InvalidReply);
        }
        if !child
            .wait()
            .await
            .map_err(|_| ProxyError::HelperUnavailable)?
            .success()
        {
            return Err(ProxyError::HelperUnavailable);
        }
        decode(&raw, target)
    })
    .await
    .unwrap_or(Err(ProxyError::HelperUnavailable));
    if result.is_err() {
        let _ = child.start_kill();
        let _ = child.wait().await;
    }
    result
}

fn decode(raw: &[u8], target: &str) -> Result<Option<String>, ProxyError> {
    let reply: Reply = serde_json::from_slice(raw).map_err(|_| ProxyError::InvalidReply)?;
    if reply.format_version != 1 || reply.target != target {
        return Err(ProxyError::InvalidReply);
    }
    if let Some(code) = reply.error_code {
        if !reply.proxy_url.is_null() {
            return Err(ProxyError::InvalidReply);
        }
        return Err(match code.as_str() {
            "automatic_proxy_unsupported" => ProxyError::AutomaticUnsupported,
            "proxy_resolution_failed" => ProxyError::ResolutionFailed,
            "proxy_endpoint_too_large" => ProxyError::EndpointUnsupported,
            _ => ProxyError::InvalidReply,
        });
    }
    match reply.proxy_url {
        serde_json::Value::Null => Ok(None),
        serde_json::Value::String(raw) => {
            let endpoint =
                reqwest::Url::parse(&raw).map_err(|_| ProxyError::EndpointUnsupported)?;
            if raw.len() > 32 * 1024
                || endpoint.host_str().is_none()
                || !matches!(endpoint.scheme(), "http" | "https" | "socks5" | "socks5h")
                || !matches!(endpoint.path(), "" | "/")
                || endpoint.query().is_some()
                || endpoint.fragment().is_some()
            {
                return Err(ProxyError::EndpointUnsupported);
            }
            Ok(Some(raw))
        }
        _ => Err(ProxyError::InvalidReply),
    }
}

#[cfg(test)]
mod tests;
