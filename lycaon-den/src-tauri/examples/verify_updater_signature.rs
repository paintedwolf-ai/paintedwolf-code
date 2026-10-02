use std::{env, fs, path::Path};

use base64::{engine::general_purpose::STANDARD, Engine as _};
use minisign_verify::{PublicKey, Signature};
use serde_json::Value;

fn decode_embedded(value: &str, label: &str) -> Result<String, String> {
    let bytes = STANDARD
        .decode(value.trim())
        .map_err(|err| format!("{label} is not base64: {err}"))?;
    String::from_utf8(bytes).map_err(|err| format!("{label} is not UTF-8: {err}"))
}

/// Reads the `version` field of a trusted comment written as tab-separated
/// `key:value` pairs, the binding the updater checks against its feed.
fn signed_version(trusted_comment: &str) -> Option<&str> {
    trusted_comment
        .split('\t')
        .find_map(|field| field.strip_prefix("version:"))
}

fn run(
    artifact: &Path,
    signature_path: &Path,
    config_path: &Path,
    version: &str,
) -> Result<(), String> {
    let config: Value = serde_json::from_slice(
        &fs::read(config_path).map_err(|err| format!("read config: {err}"))?,
    )
    .map_err(|err| format!("parse config: {err}"))?;
    let encoded_public_key = config
        .pointer("/plugins/updater/pubkey")
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| "config has no updater public key".to_string())?;
    let public_key = PublicKey::decode(&decode_embedded(encoded_public_key, "updater public key")?)
        .map_err(|err| format!("decode updater public key: {err}"))?;

    let encoded_signature =
        fs::read_to_string(signature_path).map_err(|err| format!("read signature: {err}"))?;
    let signature = Signature::decode(&decode_embedded(&encoded_signature, "updater signature")?)
        .map_err(|err| format!("decode updater signature: {err}"))?;
    let bytes = fs::read(artifact).map_err(|err| format!("read artifact: {err}"))?;
    public_key
        .verify(&bytes, &signature, true)
        .map_err(|err| format!("updater signature verification failed: {err}"))?;
    match signed_version(signature.trusted_comment()) {
        Some(signed) if signed == version => Ok(()),
        Some(signed) => Err(format!(
            "updater signature is bound to version {signed}, not {version}"
        )),
        None => Err("updater signature is not bound to a version".into()),
    }
}

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() != 5 {
        eprintln!("usage: verify_updater_signature ARTIFACT SIGNATURE TAURI_CONFIG VERSION");
        std::process::exit(2);
    }
    if let Err(err) = run(
        Path::new(&args[1]),
        Path::new(&args[2]),
        Path::new(&args[3]),
        &args[4],
    ) {
        eprintln!("error: {err}");
        std::process::exit(1);
    }
    eprintln!(
        "updater artifact signature matches the release signing public key and version {}",
        args[4]
    );
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    fn public_document(key: &SigningKey, number: u8) -> String {
        let mut bytes = b"Ed".to_vec();
        bytes.extend_from_slice(&[number; 8]);
        bytes.extend_from_slice(key.verifying_key().as_bytes());
        STANDARD.encode(format!(
            "untrusted comment: test key\n{}\n",
            STANDARD.encode(bytes)
        ))
    }

    fn signature_document(key: &SigningKey, number: u8, bytes: &[u8], version: &str) -> String {
        signature_with_comment(
            key,
            number,
            bytes,
            &format!("timestamp:0\tfile:bridge.bin\tversion:{version}"),
        )
    }

    fn signature_with_comment(
        key: &SigningKey,
        number: u8,
        bytes: &[u8],
        trusted_comment: &str,
    ) -> String {
        let signature = key.sign(bytes).to_bytes();
        let mut global_message = signature.to_vec();
        global_message.extend_from_slice(trusted_comment.as_bytes());
        let global = key.sign(&global_message);
        let mut payload = b"Ed".to_vec();
        payload.extend_from_slice(&[number; 8]);
        payload.extend_from_slice(&signature);
        STANDARD.encode(format!(
            "untrusted comment: test signature\n{}\ntrusted comment: {trusted_comment}\n{}\n",
            STANDARD.encode(payload),
            STANDARD.encode(global.to_bytes())
        ))
    }

    #[test]
    fn bridge_artifacts_verify_only_with_the_source_generation_and_preserve_skipped_routes() {
        let directory = env::temp_dir().join(format!("updater-bridge-{}", uuid::Uuid::new_v4()));
        fs::create_dir(&directory).unwrap();
        let artifact = directory.join("bridge.bin");
        let signature = directory.join("bridge.sig");
        let config = directory.join("config.json");
        let keys = [
            SigningKey::from_bytes(&[1; 32]),
            SigningKey::from_bytes(&[2; 32]),
            SigningKey::from_bytes(&[3; 32]),
        ];
        for index in 0..2 {
            let bytes = serde_json::to_vec(&serde_json::json!({"embedded_generation": index + 2,
                "public_key": public_document(&keys[index + 1], (index + 2) as u8)}))
            .unwrap();
            fs::write(&artifact, &bytes).unwrap();
            fs::write(
                &signature,
                signature_document(&keys[index], (index + 1) as u8, &bytes, "2.0.0"),
            )
            .unwrap();
            fs::write(
                &config,
                serde_json::to_vec(&serde_json::json!({"plugins":{"updater":{
                "pubkey": public_document(&keys[index], (index + 1) as u8)}}}))
                .unwrap(),
            )
            .unwrap();
            run(&artifact, &signature, &config, "2.0.0").expect("source generation accepts its bridge");
            fs::write(&artifact, b"failed or substituted bridge download").unwrap();
            assert!(run(&artifact, &signature, &config, "2.0.0").is_err());
            fs::write(&artifact, &bytes).unwrap();
            fs::write(
                &signature,
                signature_document(&keys[index + 1], (index + 2) as u8, &bytes, "2.0.0"),
            )
            .unwrap();
            assert!(
                run(&artifact, &signature, &config, "2.0.0").is_err(),
                "source feed cannot skip its bridge signature"
            );
        }
        fs::remove_dir_all(directory).unwrap();
    }

    #[test]
    fn signatures_must_bind_the_announced_release_version() {
        let directory = env::temp_dir().join(format!("updater-version-{}", uuid::Uuid::new_v4()));
        fs::create_dir(&directory).unwrap();
        let artifact = directory.join("app.tar.gz");
        let signature = directory.join("app.tar.gz.sig");
        let config = directory.join("config.json");
        let key = SigningKey::from_bytes(&[4; 32]);
        fs::write(&artifact, b"release archive").unwrap();
        fs::write(
            &config,
            serde_json::to_vec(&serde_json::json!({"plugins":{"updater":{
            "pubkey": public_document(&key, 4)}}}))
            .unwrap(),
        )
        .unwrap();
        fs::write(
            &signature,
            signature_document(&key, 4, b"release archive", "1.0.0-rc.4"),
        )
        .unwrap();
        run(&artifact, &signature, &config, "1.0.0-rc.4").expect("release version binding");
        // The macOS bundle version drops the prerelease; it does not identify the release.
        let err = run(&artifact, &signature, &config, "1.0.0").unwrap_err();
        assert!(err.contains("bound to version 1.0.0-rc.4"), "{err}");
        fs::write(
            &signature,
            signature_with_comment(&key, 4, b"release archive", "timestamp:0\tfile:app.tar.gz"),
        )
        .unwrap();
        let err = run(&artifact, &signature, &config, "1.0.0-rc.4").unwrap_err();
        assert!(err.contains("not bound to a version"), "{err}");
        fs::remove_dir_all(directory).unwrap();
    }
}
