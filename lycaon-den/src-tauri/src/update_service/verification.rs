//! Minisign verification of release artifacts and of the signed feed pointer.
//!
//! Every signature's trusted comment is written by `tauri signer sign` as
//! `timestamp:<unix>\tfile:<name>\tversion:<product version>`. The artifact signature binds
//! the offered version; the feed signature additionally binds the pointer's channel and key
//! generation through its file name and orders pointers by timestamp.
use base64::{engine::general_purpose::STANDARD, Engine as _};
use minisign_verify::{PublicKey, Signature};
use std::{
    fs::File,
    io::{Read, Seek, SeekFrom},
    path::Path,
};

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Limits {
    pub format_version: u8,
    pub max_download_bytes: u64,
    pub max_expanded_bytes: u64,
    pub max_archive_entries: usize,
    pub disk_reserve_bytes: u64,
}
pub fn limits() -> &'static Limits {
    static LIMITS: std::sync::LazyLock<Limits> = std::sync::LazyLock::new(|| {
        let limits: Limits =
            serde_json::from_str(include_str!("../../../../packaging/update-limits.json"))
                .expect("compiled update limits");
        assert_eq!(limits.format_version, 1);
        limits
    });
    &LIMITS
}
fn decode(value: &str) -> Result<String, String> {
    String::from_utf8(STANDARD.decode(value.trim()).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())
}
/// The fields a release signature commits to; every signature binds a version.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SignedComment {
    pub timestamp: Option<u64>,
    pub file: Option<String>,
    pub version: String,
}
/// What a verified feed pointer is bound to.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FeedBinding {
    pub timestamp: u64,
    pub version: String,
}
pub fn signed_comment(signature: &Signature) -> Result<SignedComment, String> {
    let mut timestamp = None;
    let mut file = None;
    let mut version = None;
    for field in signature.trusted_comment().split('\t') {
        let Some((name, value)) = field.split_once(':') else {
            return Err("Signature comment has an unlabelled field".into());
        };
        let slot = match name {
            "timestamp" => &mut timestamp,
            "file" => &mut file,
            "version" => &mut version,
            _ => return Err(format!("Signature comment has an unknown field: {name}")),
        };
        if slot.replace(value.to_string()).is_some() {
            return Err(format!("Signature comment repeats {name}"));
        }
    }
    let Some(version) = version.filter(|version| !version.is_empty()) else {
        return Err("Signature comment does not bind a version".into());
    };
    let timestamp = timestamp
        .map(|value| {
            value
                .parse::<u64>()
                .map_err(|_| "Signature timestamp is not a number".to_string())
        })
        .transpose()?;
    if file.as_deref() == Some("") {
        return Err("Signature comment has an empty file binding".into());
    }
    Ok(SignedComment {
        timestamp,
        file,
        version,
    })
}
fn decode_signature(encoded_signature: &str) -> Result<Signature, String> {
    Signature::decode(&decode(encoded_signature)?).map_err(|e| e.to_string())
}
fn decode_key(encoded_key: &str) -> Result<PublicKey, String> {
    PublicKey::decode(&decode(encoded_key)?).map_err(|e| e.to_string())
}
pub fn verify(
    artifact: &Path,
    encoded_signature: &str,
    encoded_key: &str,
    version: &str,
) -> Result<(), String> {
    let mut file = File::open(artifact).map_err(|e| e.to_string())?;
    verify_file(&mut file, encoded_signature, encoded_key, version)
}
pub fn verify_file(
    file: &mut File,
    encoded_signature: &str,
    encoded_key: &str,
    version: &str,
) -> Result<(), String> {
    file.seek(SeekFrom::Start(0)).map_err(|e| e.to_string())?;
    let key = decode_key(encoded_key)?;
    let signature = decode_signature(encoded_signature)?;
    if signed_comment(&signature)?.version != version {
        return Err("Artifact signature does not bind the offered version".into());
    }
    let mut stream = key.verify_stream(&signature).map_err(|e| e.to_string())?;
    if !file.metadata().map_err(|e| e.to_string())?.is_file() {
        return Err("Artifact is not a regular file".into());
    }
    let mut buffer = [0u8; 64 * 1024];
    let mut total = 0u64;
    loop {
        let n = file.read(&mut buffer).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        total += n as u64;
        if total > limits().max_download_bytes {
            return Err("Artifact exceeds the download limit".into());
        }
        stream.update(&buffer[..n]);
    }
    stream.finalize().map_err(|e| e.to_string())
}
/// Verifies a feed pointer against the feed key and returns what its signature binds.
pub fn verify_feed(
    manifest: &[u8],
    encoded_signature: &str,
    encoded_key: &str,
    expected_file: &str,
) -> Result<FeedBinding, String> {
    let key = decode_key(encoded_key)?;
    let signature = decode_signature(encoded_signature)?;
    let comment = signed_comment(&signature)?;
    if comment.file.as_deref() != Some(expected_file) {
        return Err(format!(
            "Feed signature is bound to {} rather than {expected_file}",
            comment.file.as_deref().unwrap_or("no file")
        ));
    }
    let Some(timestamp) = comment.timestamp else {
        return Err("Feed signature carries no timestamp".into());
    };
    key.verify(manifest, &signature, false)
        .map_err(|e| e.to_string())?;
    Ok(FeedBinding {
        timestamp,
        version: comment.version,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;
    fn fixture() -> Value {
        serde_json::from_str(include_str!("fixtures/signed-feed.json")).unwrap()
    }
    #[test]
    fn feed_signature_binds_bytes_file_and_version() {
        let fixture = fixture();
        let manifest = STANDARD.decode(fixture["manifest"].as_str().unwrap()).unwrap();
        let signature = fixture["signature"].as_str().unwrap();
        let key = fixture["feed_public_key"].as_str().unwrap();
        let binding = verify_feed(&manifest, signature, key, "latest-stable-key-1.json").unwrap();
        assert_eq!(binding.version, "1.2.3");
        assert!(binding.timestamp > 1_700_000_000);
        assert!(verify_feed(&manifest, signature, key, "latest-preview-key-1.json").is_err());
        let mut altered = manifest.clone();
        altered.push(b' ');
        assert!(verify_feed(&altered, signature, key, "latest-stable-key-1.json").is_err());
        let other_key = fixture["other_public_key"].as_str().unwrap();
        assert!(verify_feed(&manifest, signature, other_key, "latest-stable-key-1.json").is_err());
    }
    #[test]
    fn signed_comments_always_bind_a_version_and_feeds_need_a_file_and_timestamp() {
        let fixture = fixture();
        let signature = decode_signature(fixture["signature"].as_str().unwrap()).unwrap();
        let comment = signed_comment(&signature).unwrap();
        assert_eq!(comment.file.as_deref(), Some("latest-stable-key-1.json"));
        assert_eq!(comment.version, "1.2.3");
        let unbound = decode_signature(fixture["unbound_signature"].as_str().unwrap()).unwrap();
        assert!(signed_comment(&unbound).is_err());
        let manifest = STANDARD.decode(fixture["manifest"].as_str().unwrap()).unwrap();
        assert!(verify_feed(
            &manifest,
            fixture["unbound_signature"].as_str().unwrap(),
            fixture["feed_public_key"].as_str().unwrap(),
            "latest-stable-key-1.json"
        )
        .is_err());
    }
}
