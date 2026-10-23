use super::manifest::{Manifest, MANIFEST_LIMIT};
use std::{io::Cursor, path::Path};

#[test]
fn shared_packaging_producer_manifest_is_admitted_and_executables_are_verified() {
    let corpus = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../packaging/linux/manifest/testdata");
    for architecture in ["x86_64", "aarch64"] {
        let raw = std::fs::read(corpus.join(format!("{architecture}.json"))).unwrap();
        let manifest = Manifest::from_reader(Cursor::new(&raw), MANIFEST_LIMIT, Path::new("/opt/painted-wolf-code")).unwrap();
        assert_eq!(manifest.architecture, architecture);
        for executable in &manifest.executables {
            let name = if executable.path.starts_with("/usr/bin") { "launcher.sh".to_string() } else { format!("{architecture}.elf") };
            let mut bytes = std::fs::read(corpus.join(name)).unwrap();
            executable.verify_reader(Cursor::new(&bytes)).unwrap();
            bytes[0] ^= 1;
            assert!(executable.verify_reader(Cursor::new(bytes)).is_err());
        }
        for mutation in ["unknown version", "missing seal"] {
            let mut document: serde_json::Value = serde_json::from_slice(&raw).unwrap();
            if mutation == "unknown version" {
                document["format_version"] = serde_json::json!(99);
            } else {
                document.as_object_mut().unwrap().remove("payload");
            }
            assert!(Manifest::from_slice(&serde_json::to_vec(&document).unwrap(), Path::new("/opt/painted-wolf-code")).is_err(), "{mutation}");
        }
    }
}
