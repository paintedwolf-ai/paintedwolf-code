//! The artifact signature authenticates bytes and the offered product version.
use base64::{engine::general_purpose::STANDARD, Engine as _};
use minisign_verify::{PublicKey, Signature};
use std::{fs::File, io::Read, path::Path};

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
pub fn verify(
    artifact: &Path,
    encoded_signature: &str,
    encoded_key: &str,
    version: &str,
) -> Result<(), String> {
    let key = PublicKey::decode(&decode(encoded_key)?).map_err(|e| e.to_string())?;
    let signature = Signature::decode(&decode(encoded_signature)?).map_err(|e| e.to_string())?;
    let versions: Vec<_> = signature
        .trusted_comment()
        .split('\t')
        .filter_map(|field| field.strip_prefix("version:"))
        .collect();
    if versions != [version] {
        return Err("Artifact signature does not bind the offered version".into());
    }
    let mut stream = key.verify_stream(&signature).map_err(|e| e.to_string())?;
    let mut file = File::open(artifact).map_err(|e| e.to_string())?;
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
