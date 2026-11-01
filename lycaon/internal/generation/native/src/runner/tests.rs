use super::*;
use crate::execution::Sampling;
use serde_json::{Value, json};
use std::io::Cursor;

struct NoDevice;
impl Backend for NoDevice {
    fn forward(&mut self, _: &[u32], _: Option<&mut Sampling>) -> Result<Option<u32>> {
        panic!("unexpected device work")
    }
    fn reset(&mut self) {}
    fn save_prefix(&mut self) -> Result<()> {
        Ok(())
    }
    fn restore_prefix(&mut self) -> Result<()> {
        Ok(())
    }
    fn clear_prefix(&mut self) {}
}

fn identity() -> Identity {
    Identity {
        artifact_id: "a".repeat(64),
        backend: "fixture",
        physical_device: wire::PhysicalDevice::AppleSilicon,
    }
}

fn frames(commands: &[Value]) -> Vec<u8> {
    let mut output = Vec::new();
    for command in commands {
        protocol::write_frame(&mut output, &serde_json::to_vec(command).unwrap()).unwrap();
    }
    output
}

#[test]
fn hello_and_shutdown_do_not_load_or_probe_the_model() {
    let mut runner = Runner::new(
        PathBuf::new(),
        identity(),
        |_: &Path, _, _| -> Result<NoDevice> { panic!("unexpected load") },
    )
    .unwrap();
    let input = frames(&[
        json!({"version":1,"id":1,"command":{"method":"hello"}}),
        json!({"version":1,"id":2,"command":{"method":"advance","request_id":1}}),
        json!({"version":1,"id":3,"command":{"method":"shutdown"}}),
    ]);
    let mut output = Vec::new();
    runner.serve(Cursor::new(input), &mut output).unwrap();
    let mut output = Cursor::new(output);
    let hello: Value =
        serde_json::from_slice(&protocol::read_frame(&mut output).unwrap().unwrap()).unwrap();
    assert_eq!(hello["result"]["physical_device"]["kind"], "apple_silicon");
    let error: Value =
        serde_json::from_slice(&protocol::read_frame(&mut output).unwrap().unwrap()).unwrap();
    assert_eq!(error["status"], "error");
    assert_eq!(error["id"], 2);
    let shutdown: Value =
        serde_json::from_slice(&protocol::read_frame(&mut output).unwrap().unwrap()).unwrap();
    assert_eq!(shutdown["result"]["kind"], "shutdown");
    assert!(protocol::read_frame(&mut output).unwrap().is_none());
}

#[test]
fn malformed_version_sequence_and_fields_close_the_connection() {
    for envelope in [
        json!({"version":2,"id":1,"command":{"method":"hello"}}),
        json!({"version":1,"id":0,"command":{"method":"hello"}}),
        json!({"version":1,"id":1,"command":{"method":"hello","extra":true}}),
        json!({"version":1,"id":1,"command":{"method":"hello"},"extra":true}),
    ] {
        let mut runner =
            Runner::new(PathBuf::new(), identity(), |_: &Path, _, _| Ok(NoDevice)).unwrap();
        assert!(
            runner
                .serve(Cursor::new(frames(&[envelope])), Vec::new())
                .is_err()
        );
    }
    let duplicate = json!({"version":1,"id":1,"command":{"method":"hello"}});
    let mut runner =
        Runner::new(PathBuf::new(), identity(), |_: &Path, _, _| Ok(NoDevice)).unwrap();
    assert!(
        runner
            .serve(
                Cursor::new(frames(&[duplicate.clone(), duplicate])),
                Vec::new()
            )
            .is_err()
    );
}
