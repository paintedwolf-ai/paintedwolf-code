use std::{env, fs, path::Path};

#[cfg(test)]
use base64::{engine::general_purpose::STANDARD, Engine as _};
use painted_wolf_code_lib::update_service::verification;
use serde_json::Value;

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
    let encoded_signature = fs::read_to_string(signature_path).map_err(|e| e.to_string())?;
    verification::verify(artifact, &encoded_signature, encoded_public_key, version)
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
    #[test]
    fn bridges_bind_source_generation_bytes_and_release_version() {
        let fixtures: Vec<Value> = serde_json::from_str(include_str!(
            "../src/update_service/fixtures/signed-releases.json"
        ))
        .unwrap();
        let directory = env::temp_dir().join(format!("updater-bridge-{}", uuid::Uuid::new_v4()));
        fs::create_dir(&directory).unwrap();
        let artifact = directory.join("bridge.bin");
        let signature = directory.join("bridge.sig");
        let config = directory.join("config.json");
        for row in fixtures {
            let bytes = STANDARD.decode(row["body"].as_str().unwrap()).unwrap();
            fs::write(&artifact, &bytes).unwrap();
            fs::write(&signature, row["signature"].as_str().unwrap()).unwrap();
            fs::write(
                &config,
                serde_json::to_vec(
                    &serde_json::json!({"plugins":{"updater":{"pubkey":row["key"]}}}),
                )
                .unwrap(),
            )
            .unwrap();
            run(&artifact, &signature, &config, "2.0.0").expect("source key accepts bridge");
            assert!(run(&artifact, &signature, &config, "2.0.1").is_err());
            fs::write(&artifact, "substituted bridge").unwrap();
            assert!(run(&artifact, &signature, &config, "2.0.0").is_err());
            fs::write(&artifact, &bytes).unwrap();
            fs::write(&signature, row["unbound_signature"].as_str().unwrap()).unwrap();
            assert!(run(&artifact, &signature, &config, "2.0.0").is_err());
            fs::write(&signature, row["signature"].as_str().unwrap()).unwrap();
            fs::write(
                &config,
                serde_json::to_vec(
                    &serde_json::json!({"plugins":{"updater":{"pubkey":row["successor_key"]}}}),
                )
                .unwrap(),
            )
            .unwrap();
            assert!(run(&artifact, &signature, &config, "2.0.0").is_err());
        }
        fs::remove_dir_all(directory).unwrap();
    }
}
