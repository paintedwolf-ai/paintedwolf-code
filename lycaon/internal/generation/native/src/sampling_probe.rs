use nativegen::{
    output_contract::Contract,
    rendering::Renderer,
    sampler::{self, Rng},
};
use std::{hint::black_box, path::Path, time::Instant};

pub fn run(model: &Path) -> Result<(), Box<dyn std::error::Error>> {
    let renderer = Renderer::load(model)?;
    let contract = Contract {
        allowed_tools: vec![
            "command".into(),
            "request_tools".into(),
            "skills_read".into(),
        ],
    };
    let started = Instant::now();
    let envelope = contract.compile(renderer.tokenizer(), false)?;
    let header_build_seconds = started.elapsed().as_secs_f64();
    let mut logits: Vec<_> = (0..262144)
        .map(|id| ((id * 37 % 1001) as f32) / 100.0)
        .collect();
    for (id, value) in logits.iter_mut().enumerate() {
        if nativegen::output_contract::reserved(id as u32) {
            *value = f32::NEG_INFINITY;
        }
    }
    let mut results = Vec::new();
    for (temperature, top_k) in [(0.0, 0), (1.0, 64)] {
        for round in 0..12 {
            let mut outputs = [Vec::new(), Vec::new()];
            let mut elapsed = [0.0; 2];
            for arm in if round % 2 == 0 { [0, 1] } else { [1, 0] } {
                let mut rng = Rng::new(42);
                let start = Instant::now();
                for _ in 0..64 {
                    let token = if arm == 0 {
                        sampler::sample(
                            logits
                                .iter()
                                .enumerate()
                                .map(|(id, value)| (id as u32, *value)),
                            temperature,
                            0.95,
                            top_k,
                            &mut rng,
                        )?
                    } else {
                        sampler::sample(
                            envelope.candidates(&logits),
                            temperature,
                            0.95,
                            top_k,
                            &mut rng,
                        )?
                    };
                    outputs[arm].push(black_box(token));
                }
                elapsed[arm] = start.elapsed().as_secs_f64() / 64.0;
            }
            if outputs[0] != outputs[1] {
                return Err("sampling support changed equivalent draws".into());
            }
            results.push(serde_json::json!({"temperature": temperature, "top_k": top_k, "round": round,
                "unfiltered_seconds": elapsed[0], "envelope_seconds": elapsed[1], "exact_tokens": true}));
        }
    }
    println!(
        "{}",
        serde_json::json!({"scope":"CPU sampling only; no model or GPU execution", "header_build_seconds":header_build_seconds,
        "header_storage_bytes": envelope.storage_bytes(), "samples_per_arm":64, "results":results})
    );
    Ok(())
}
