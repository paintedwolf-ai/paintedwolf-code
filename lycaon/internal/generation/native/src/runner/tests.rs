use super::*;
use crate::execution::Sampling;
use serde_json::{Value, json};
use std::io::Cursor;

struct NoDevice;
impl Backend for NoDevice {
    fn plan(_: &std::path::Path, context: usize, chunk: usize) -> Result<crate::resources::Plan> {
        crate::resources::Plan::e4b(3 << 30, 2 << 30, context, chunk, false)
    }
    fn forward(&mut self, _: &[u32], _: Option<&mut Sampling>) -> Result<Option<u32>> {
        panic!("unexpected device work")
    }
    fn reset(&mut self) -> Result<()> {
        Ok(())
    }
    fn save_prefix(&mut self) -> Result<()> {
        Ok(())
    }
    fn restore_prefix(&mut self) -> Result<bool> {
        Ok(true)
    }
    fn clear_prefix(&mut self) -> Result<()> {
        Ok(())
    }
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
        |_: &Path, _, _, _| -> Result<NoDevice> { panic!("unexpected load") },
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
            Runner::new(PathBuf::new(), identity(), |_: &Path, _, _, _| Ok(NoDevice)).unwrap();
        assert!(
            runner
                .serve(Cursor::new(frames(&[envelope])), Vec::new())
                .is_err()
        );
    }
    let duplicate = json!({"version":1,"id":1,"command":{"method":"hello"}});
    let mut runner =
        Runner::new(PathBuf::new(), identity(), |_: &Path, _, _, _| Ok(NoDevice)).unwrap();
    assert!(
        runner
            .serve(
                Cursor::new(frames(&[duplicate.clone(), duplicate])),
                Vec::new()
            )
            .is_err()
    );
}

#[test]
fn resource_inspection_and_rejection_precede_model_loading() {
    let mut runner = Runner::new(
        PathBuf::new(),
        identity(),
        |_: &Path, _, _, _| -> Result<NoDevice> { panic!("unreserved model load") },
    )
    .unwrap();
    let mut output = Vec::new();
    runner.serve(Cursor::new(frames(&[
        json!({"version":1,"id":1,"command":{"method":"inspect","context_tokens":4096,"prefill_chunk":128}}),
        json!({"version":1,"id":2,"command":{"method":"load","context_tokens":4096,"prefill_chunk":128,"device_budget_bytes":1,"host_budget_bytes":1}}),
    ])), &mut output).unwrap();
    let mut output = Cursor::new(output);
    let plan: Value =
        serde_json::from_slice(&protocol::read_frame(&mut output).unwrap().unwrap()).unwrap();
    assert!(
        plan["result"]["plan"]["device_reservation_bytes"]
            .as_u64()
            .unwrap()
            > 3 << 30
    );
    let rejection: Value =
        serde_json::from_slice(&protocol::read_frame(&mut output).unwrap().unwrap()).unwrap();
    assert_eq!(rejection["code"], "capacity");
}
