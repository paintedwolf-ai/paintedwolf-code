use super::*;
use serde_json::{json, Value};

fn entry() -> Value {
    json!({
        "version": 1, "supervisor_pid": 123, "uid": 1000, "gid": 1000, "groups": [1000],
        "generation": {
            "version": 1, "generation_root": "/var/lib/painted-wolf/generations/selected",
            "logical_install_root": "/opt/painted-wolf-code",
            "manifest_sha256": "a".repeat(64)
        },
        "image": {
            "path": "/var/lib/painted-wolf/generations/selected/bin/painted-wolf-code",
            "sha256": "b".repeat(64), "size": 1234
        },
        "arguments": [], "environment": []
    })
}

fn parse(value: &Value) -> Result<Generation, GenerationError> {
    decode(
        &serde_json::to_vec(value).unwrap(),
        123,
        1000,
        1000,
        &[1000],
    )
}

#[test]
fn authenticated_selection_uses_the_namespace_logical_root() {
    assert_eq!(
        parse(&entry()).unwrap().logical_root,
        Path::new("/opt/painted-wolf-code")
    );
}

#[test]
fn selected_image_and_policy_must_describe_the_same_generation() {
    for (pointer, invalid) in [
        ("/version", json!(2)),
        ("/generation/version", json!(2)),
        ("/generation/generation_root", json!("/other")),
        ("/generation/logical_install_root", json!("relative")),
        ("/generation/logical_install_root", json!("/opt/../other")),
        ("/generation/logical_install_root", json!("/opt//other")),
        ("/generation/logical_install_root", json!("/")),
        ("/generation/manifest_sha256", json!("unknown")),
        ("/image/size", json!(0)),
        ("/image/sha256", json!("B".repeat(64))),
        (
            "/image/path",
            json!("/opt/painted-wolf-code/bin/painted-wolf-code"),
        ),
    ] {
        let mut value = entry();
        *value.pointer_mut(pointer).unwrap() = invalid;
        assert_eq!(
            parse(&value),
            Err(GenerationError::InvalidFrame),
            "{pointer}"
        );
    }
    let mut value = entry();
    value["generation"]["unknown_authority"] = json!(true);
    assert_eq!(parse(&value), Err(GenerationError::InvalidFrame));
}

#[test]
fn frame_must_belong_to_the_actual_broker_and_login_identity() {
    let mut value = entry();
    value["supervisor_pid"] = json!(456);
    assert_eq!(parse(&value), Err(GenerationError::BrokerMismatch));
    for (field, invalid) in [
        ("uid", json!(0)),
        ("gid", json!(2000)),
        ("groups", json!([])),
    ] {
        let mut value = entry();
        value[field] = invalid;
        assert_eq!(parse(&value), Err(GenerationError::IdentityMismatch));
    }
    let mut raw = serde_json::to_vec(&entry()).unwrap();
    raw.extend_from_slice(b"{}");
    assert_eq!(
        decode(&raw, 123, 1000, 1000, &[1000]),
        Err(GenerationError::InvalidFrame)
    );
}
