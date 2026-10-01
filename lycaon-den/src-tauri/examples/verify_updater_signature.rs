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

fn run(artifact: &Path, signature_path: &Path, config_path: &Path) -> Result<(), String> {
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
        .map_err(|err| format!("updater signature verification failed: {err}"))
}

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() != 4 {
        eprintln!("usage: verify_updater_signature ARTIFACT SIGNATURE TAURI_CONFIG");
        std::process::exit(2);
    }
    if let Err(err) = run(
        Path::new(&args[1]),
        Path::new(&args[2]),
        Path::new(&args[3]),
    ) {
        eprintln!("error: {err}");
        std::process::exit(1);
    }
    eprintln!("updater artifact signature matches the release signing public key");
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

    fn signature_document(key: &SigningKey, number: u8, bytes: &[u8]) -> String {
        let signature = key.sign(bytes).to_bytes();
        let mut global_message = signature.to_vec();
        global_message.extend_from_slice(b"bridge fixture");
        let global = key.sign(&global_message);
        let mut payload = b"Ed".to_vec();
        payload.extend_from_slice(&[number; 8]);
        payload.extend_from_slice(&signature);
        STANDARD.encode(format!(
            "untrusted comment: test signature\n{}\ntrusted comment: bridge fixture\n{}\n",
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
                signature_document(&keys[index], (index + 1) as u8, &bytes),
            )
            .unwrap();
            fs::write(
                &config,
                serde_json::to_vec(&serde_json::json!({"plugins":{"updater":{
                "pubkey": public_document(&keys[index], (index + 1) as u8)}}}))
                .unwrap(),
            )
            .unwrap();
            run(&artifact, &signature, &config).expect("source generation accepts its bridge");
            fs::write(&artifact, b"failed or substituted bridge download").unwrap();
            assert!(run(&artifact, &signature, &config).is_err());
            fs::write(&artifact, &bytes).unwrap();
            fs::write(
                &signature,
                signature_document(&keys[index + 1], (index + 2) as u8, &bytes),
            )
            .unwrap();
            assert!(
                run(&artifact, &signature, &config).is_err(),
                "source feed cannot skip its bridge signature"
            );
        }
        fs::remove_dir_all(directory).unwrap();
    }
}
