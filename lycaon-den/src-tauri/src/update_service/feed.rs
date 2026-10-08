//! Signed feed discovery: the channel pointer, its signature, and the offer it carries.
//!
//! The pointer is fetched directly from the release origin without proxies or redirects,
//! verified with the embedded feed key, and ordered by its signed timestamp before any
//! field is read. The artifact signature inside it is verified again when bytes arrive.
use super::{
    persistence, verification, Candidate, Failure, RolloutEligibility, UpdateChannel, UpdateError,
    CHECK_REQUEST_TIMEOUT, DOWNLOAD_ORIGIN,
};
use futures_util::StreamExt;
use serde::Deserialize;
use std::collections::BTreeMap;

const MAX_MANIFEST_BYTES: u64 = 1024 * 1024;
const MAX_SIGNATURE_BYTES: u64 = 16 * 1024;

/// The artifact key generation this build trusts and its public key.
pub(super) fn embedded_key() -> (u64, String) {
    let generation = embedded_generation();
    (
        generation["generation"].as_u64().expect("key generation"),
        generation["public_key"]
            .as_str()
            .expect("public key string")
            .into(),
    )
}
/// The feed key that signs this generation's channel pointers.
pub(super) fn feed_key() -> String {
    embedded_generation()["feed_public_key"]
        .as_str()
        .expect("feed public key string")
        .into()
}
fn embedded_generation() -> serde_json::Value {
    let registry: serde_json::Value =
        serde_json::from_str(include_str!("../../../../packaging/update-keys.json"))
            .expect("compiled updater key registry");
    let number = registry["embedded_generation"]
        .as_u64()
        .expect("embedded key generation");
    registry["generations"]
        .as_array()
        .expect("key generations")
        .iter()
        .find(|row| row["generation"].as_u64() == Some(number))
        .expect("embedded key generation row")
        .clone()
}
/// The updater platform key of this build, matching `packaging/release-platforms.json`.
pub fn target() -> &'static str {
    match (std::env::consts::OS, std::env::consts::ARCH) {
        ("macos", "aarch64") => "darwin-aarch64",
        ("macos", "x86_64") => "darwin-x86_64",
        ("linux", "x86_64") => "linux-x86_64",
        ("linux", "aarch64") => "linux-aarch64",
        ("windows", "x86_64") => "windows-x86_64",
        _ => "unsupported",
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Manifest {
    version: String,
    notes: String,
    #[serde(default)]
    pub_date: String,
    platforms: BTreeMap<String, PlatformEntry>,
    update_keys: UpdateKeys,
    #[serde(default)]
    withdrawn: Option<bool>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct PlatformEntry {
    signature: String,
    url: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct UpdateKeys {
    signing_generation: u64,
    embedded_generation: u64,
    signing_key_fingerprint: String,
    embedded_key_fingerprint: String,
}

/// A release the feed currently offers to this build.
#[derive(Debug)]
pub struct Offer {
    pub candidate: Candidate,
    /// Seconds since the manifest's publication, when its date is well formed.
    pub published_age_secs: Option<u64>,
}

#[derive(Debug, Clone)]
pub enum FeedFailure {
    Transient(UpdateError),
    Rejected(UpdateError),
    State(UpdateError),
}
impl FeedFailure {
    pub fn error(self) -> UpdateError {
        match self {
            Self::Transient(error) | Self::State(error) => error,
            Self::Rejected(error) => UpdateError::new(Failure::FeedRejected, error),
        }
    }
}
impl From<UpdateError> for FeedFailure {
    fn from(error: UpdateError) -> Self {
        match error.code {
            Failure::CheckFailed => Self::Transient(error),
            Failure::InvalidRelease | Failure::InvalidVersion => Self::Rejected(error),
            _ => Self::State(error),
        }
    }
}

pub async fn fetch(
    channel: UpdateChannel,
    running_version: &str,
) -> Result<Option<Offer>, FeedFailure> {
    fetch_with_deadline(channel, running_version, CHECK_REQUEST_TIMEOUT).await
}

pub async fn fetch_with_deadline(
    channel: UpdateChannel,
    running_version: &str,
    deadline: std::time::Duration,
) -> Result<Option<Offer>, FeedFailure> {
    let deadline = tokio::time::Instant::now() + deadline;
    let running = semver::Version::parse(running_version)
        .map_err(|e| UpdateError::new(Failure::InvalidVersion, e))?;
    let client = reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(CHECK_REQUEST_TIMEOUT)
        .build()
        .map_err(|e| UpdateError::new(Failure::CheckFailed, e))?;
    let endpoint = channel.endpoint();
    let (manifest, signature) = tokio::time::timeout_at(deadline, async {
        let manifest = fetch_bytes(&client, &endpoint, MAX_MANIFEST_BYTES).await?;
        let signature =
            fetch_bytes(&client, &format!("{endpoint}.sig"), MAX_SIGNATURE_BYTES).await?;
        Ok::<_, UpdateError>((manifest, signature))
    })
    .await
    .map_err(|_| {
        UpdateError::new(
            Failure::CheckFailed,
            "The update feed did not respond in time",
        )
    })??;
    let signature =
        String::from_utf8(signature).map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    let (generation, _) = embedded_key();
    let feed_name = channel.feed_name();
    let comment = verification::verify_feed(&manifest, &signature, &feed_key(), &feed_name)
        .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    tokio::time::timeout_at(
        deadline,
        super::feed_state::accept(
            &persistence::preferences_dir()?,
            &feed_name,
            comment.timestamp,
        ),
    )
    .await
    .map_err(|_| UpdateError::new(Failure::StateUnavailable, "The update feed record is busy"))??;
    offer(
        &manifest,
        &comment.version,
        channel,
        generation,
        &running,
        super::now(),
    )
    .map_err(Into::into)
}

async fn fetch_bytes(
    client: &reqwest::Client,
    url: &str,
    cap: u64,
) -> Result<Vec<u8>, UpdateError> {
    let response = client
        .get(url)
        .header("Cache-Control", "no-cache, no-store")
        .send()
        .await
        .map_err(UpdateError::transport)?;
    let status = response.status();
    if !status.is_success() {
        let code = if status.is_server_error() {
            Failure::CheckFailed
        } else {
            Failure::InvalidRelease
        };
        return Err(UpdateError::new(
            code,
            format!("{url} returned HTTP {status}"),
        ));
    }
    if response.content_length().is_some_and(|length| length > cap) {
        return Err(UpdateError::new(
            Failure::InvalidRelease,
            format!("{url} exceeds its size limit"),
        ));
    }
    let mut bytes = Vec::new();
    let mut stream = response.bytes_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(UpdateError::transport)?;
        if bytes.len() as u64 + chunk.len() as u64 > cap {
            return Err(UpdateError::new(
                Failure::InvalidRelease,
                format!("{url} exceeds its size limit"),
            ));
        }
        bytes.extend_from_slice(&chunk);
    }
    Ok(bytes)
}

/// Interprets a verified manifest for this build. `now` is the unix time used for the rollout age.
fn offer(
    manifest: &[u8],
    signed_version: &str,
    channel: UpdateChannel,
    generation: u64,
    running: &semver::Version,
    now: u64,
) -> Result<Option<Offer>, UpdateError> {
    let manifest: Manifest = serde_json::from_slice(manifest)
        .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    if manifest.version != signed_version {
        return Err(UpdateError::new(
            Failure::InvalidRelease,
            "Feed signature binds a different version than the manifest announces",
        ));
    }
    if manifest.update_keys.signing_generation != generation
        || manifest.update_keys.embedded_generation < generation
        || manifest.update_keys.embedded_generation > generation + 1
        || !persistence::hex_digest(&manifest.update_keys.signing_key_fingerprint)
        || !persistence::hex_digest(&manifest.update_keys.embedded_key_fingerprint)
    {
        return Err(UpdateError::new(
            Failure::InvalidRelease,
            "Feed names a key generation this build does not trust",
        ));
    }
    match manifest.withdrawn {
        Some(true) => return Ok(None),
        Some(false) => {
            return Err(UpdateError::new(
                Failure::InvalidRelease,
                "Feed carries an invalid withdrawal marker",
            ))
        }
        None => {}
    }
    let version = semver::Version::parse(&manifest.version)
        .map_err(|e| UpdateError::new(Failure::InvalidVersion, e))?;
    if channel == UpdateChannel::Stable && !version.pre.is_empty() {
        return Err(UpdateError::new(
            Failure::InvalidRelease,
            "The stable feed offered a prerelease",
        ));
    }
    if version <= *running {
        return Ok(None);
    }
    let platform = target();
    let entry = manifest.platforms.get(platform).ok_or_else(|| {
        UpdateError::new(
            Failure::InvalidRelease,
            format!("Feed has no artifact for {platform}"),
        )
    })?;
    if !entry
        .url
        .strip_prefix(DOWNLOAD_ORIGIN)
        .is_some_and(|path| path.starts_with('/'))
    {
        return Err(UpdateError::new(
            Failure::InvalidRelease,
            "Feed artifact is not served by the release origin",
        ));
    }
    if manifest.notes.trim().is_empty() || entry.signature.trim().is_empty() {
        return Err(UpdateError::new(
            Failure::InvalidRelease,
            "Feed entry is incomplete",
        ));
    }
    let mut candidate = Candidate {
        release_id: String::new(),
        version: manifest.version,
        channel,
        platform: platform.into(),
        signing_generation: generation,
        artifact_url: entry.url.clone(),
        artifact_signature: entry.signature.clone(),
        notes: Some(manifest.notes),
        rollout_eligibility: RolloutEligibility::Eligible,
    };
    candidate.release_id = candidate.identity();
    let published_age_secs = parse_rfc3339(&manifest.pub_date)
        .map(|published| (now as i64).saturating_sub(published).max(0) as u64);
    Ok(Some(Offer {
        candidate,
        published_age_secs,
    }))
}

/// Parses `YYYY-MM-DDThh:mm:ss[.frac](Z|±hh:mm)` into unix seconds.
pub(super) fn parse_rfc3339(value: &str) -> Option<i64> {
    let value = value.trim();
    let (date, rest) = value.split_once(['T', 't'])?;
    let mut date_parts = date.split('-');
    let year: i64 = date_parts.next()?.parse().ok()?;
    let month: u32 = date_parts.next()?.parse().ok()?;
    let day: u32 = date_parts.next()?.parse().ok()?;
    if date_parts.next().is_some() || !(1..=12).contains(&month) || !(1..=31).contains(&day) {
        return None;
    }
    let offset_at = rest.find(['Z', 'z', '+', '-'])?;
    let (time, offset) = rest.split_at(offset_at);
    let time = time.split('.').next()?;
    let mut time_parts = time.split(':');
    let hour: i64 = time_parts.next()?.parse().ok()?;
    let minute: i64 = time_parts.next()?.parse().ok()?;
    let second: i64 = time_parts.next()?.parse().ok()?;
    if time_parts.next().is_some() || hour > 23 || minute > 59 || second > 60 {
        return None;
    }
    let offset_secs = match offset {
        "Z" | "z" => 0,
        _ => {
            let sign = if offset.starts_with('-') { -1 } else { 1 };
            let (oh, om) = offset[1..].split_once(':')?;
            let oh: i64 = oh.parse().ok()?;
            let om: i64 = om.parse().ok()?;
            if oh > 23 || om > 59 {
                return None;
            }
            sign * (oh * 3600 + om * 60)
        }
    };
    // Days from civil date (Howard Hinnant's algorithm).
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (i64::from(month) + 9) % 12;
    let doy = (153 * mp + 2) / 5 + i64::from(day) - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    Some(days * 86_400 + hour * 3600 + minute * 60 + second - offset_secs)
}

#[cfg(test)]
mod tests {
    use super::*;
    fn manifest(version: &str, extra: &str) -> Vec<u8> {
        format!(
            r#"{{"version":"{version}","notes":"Notes.","pub_date":"2026-02-01T00:00:00Z",
            "platforms":{{"{}":{{"signature":"sig","url":"{DOWNLOAD_ORIGIN}/releases/v{version}/app.tar.gz"}}}},
            "update_keys":{{"signing_generation":1,"embedded_generation":1,
            "signing_key_fingerprint":"{}","embedded_key_fingerprint":"{}"}}{extra}}}"#,
            target(),
            "a".repeat(64),
            "a".repeat(64)
        )
        .into_bytes()
    }
    fn running() -> semver::Version {
        semver::Version::parse("1.0.0").unwrap()
    }
    #[test]
    fn newer_signed_release_becomes_a_candidate_with_its_publication_age() {
        let published = parse_rfc3339("2026-02-01T00:00:00Z").unwrap() as u64;
        let offer = offer(
            &manifest("1.1.0", ""),
            "1.1.0",
            UpdateChannel::Stable,
            1,
            &running(),
            published + 3_600,
        )
        .unwrap()
        .unwrap();
        assert_eq!(offer.candidate.version, "1.1.0");
        assert_eq!(offer.published_age_secs, Some(3_600));
        assert!(offer.candidate.valid_identity());
        assert_eq!(offer.candidate.notes.as_deref(), Some("Notes."));
    }
    #[test]
    fn signed_version_manifest_version_and_running_version_are_reconciled() {
        assert_eq!(
            offer(
                &manifest("1.1.0", ""),
                "1.1.1",
                UpdateChannel::Stable,
                1,
                &running(),
                0
            )
            .unwrap_err()
            .code,
            Failure::InvalidRelease
        );
        assert!(offer(
            &manifest("1.0.0", ""),
            "1.0.0",
            UpdateChannel::Stable,
            1,
            &running(),
            0
        )
        .unwrap()
        .is_none());
        assert!(offer(
            &manifest("0.9.0", ""),
            "0.9.0",
            UpdateChannel::Stable,
            1,
            &running(),
            0
        )
        .unwrap()
        .is_none());
    }
    #[test]
    fn withdrawn_prerelease_and_foreign_feeds_are_handled() {
        assert!(offer(
            &manifest("1.1.0", r#","withdrawn":true"#),
            "1.1.0",
            UpdateChannel::Stable,
            1,
            &running(),
            0
        )
        .unwrap()
        .is_none());
        for (version, channel, generation) in [
            ("1.1.0-rc.1", UpdateChannel::Stable, 1),
            ("1.1.0", UpdateChannel::Stable, 2),
        ] {
            assert_eq!(
                offer(
                    &manifest(version, ""),
                    version,
                    channel,
                    generation,
                    &running(),
                    0
                )
                .unwrap_err()
                .code,
                Failure::InvalidRelease
            );
        }
        assert!(offer(
            &manifest("1.1.0-rc.1", ""),
            "1.1.0-rc.1",
            UpdateChannel::Preview,
            1,
            &running(),
            0
        )
        .unwrap()
        .is_some());
        let foreign = String::from_utf8(manifest("1.1.0", ""))
            .unwrap()
            .replace(DOWNLOAD_ORIGIN, "https://example.test");
        assert_eq!(
            offer(
                foreign.as_bytes(),
                "1.1.0",
                UpdateChannel::Stable,
                1,
                &running(),
                0
            )
            .unwrap_err()
            .code,
            Failure::InvalidRelease
        );
    }
    #[test]
    fn rfc3339_dates_convert_to_unix_seconds() {
        assert_eq!(parse_rfc3339("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_rfc3339("2026-02-01T00:00:00Z"), Some(1_769_904_000));
        assert_eq!(
            parse_rfc3339("2026-02-01T02:30:00.250+02:30"),
            Some(1_769_904_000)
        );
        assert_eq!(
            parse_rfc3339("2026-01-31T23:00:00-01:00"),
            Some(1_769_904_000)
        );
        assert_eq!(parse_rfc3339("2026-02-01"), None);
        assert_eq!(parse_rfc3339("2026-13-01T00:00:00Z"), None);
    }
    #[test]
    fn build_target_matches_the_release_platform_catalog() {
        let catalog: serde_json::Value =
            serde_json::from_str(include_str!("../../../../packaging/release-platforms.json"))
                .unwrap();
        let keys: Vec<&str> = catalog["platforms"]
            .as_array()
            .unwrap()
            .iter()
            .map(|row| row["updater_key"].as_str().unwrap())
            .collect();
        assert!(
            keys.contains(&target()),
            "{} is not a release platform",
            target()
        );
    }
}
