use mlx_rs::{memory, ops::indexing::*};
use nativegen::mlx_engine::{Cache, Model, Result};
use serde::Deserialize;
use serde_json::json;
use std::path::Path;
use std::time::Instant;

#[derive(Deserialize)]
struct Specification {
    prompt_tokens: Vec<i32>,
    generation_tokens: usize,
    trials: usize,
    ple: String,
    prefill_chunk: usize,
    kv_bits: u8,
}

fn usage() -> libc::rusage {
    let mut value = std::mem::MaybeUninit::uninit();
    // getrusage initializes the complete structure on success.
    unsafe {
        assert_eq!(libc::getrusage(libc::RUSAGE_SELF, value.as_mut_ptr()), 0);
        value.assume_init()
    }
}

fn measure(model: &Model, spec: &Specification) -> Result<serde_json::Value> {
    let mut cache = Cache::default();
    memory::reset_peak_memory()?;
    let before = usage();
    let started = Instant::now();
    let mut prefill_step: f64 = 0.0;
    for chunk in spec.prompt_tokens[..spec.prompt_tokens.len() - 1].chunks(spec.prefill_chunk) {
        let step = Instant::now();
        let _ = model.forward(chunk, &mut cache)?;
        cache.evaluate()?;
        prefill_step = prefill_step.max(step.elapsed().as_secs_f64());
    }
    let step = Instant::now();
    let logits = model.forward(
        &spec.prompt_tokens[spec.prompt_tokens.len() - 1..],
        &mut cache,
    )?;
    cache.evaluate_with(&[&logits])?;
    prefill_step = prefill_step.max(step.elapsed().as_secs_f64());
    let prefill = started.elapsed().as_secs_f64();
    let mut token = argmax(logits.index((0, -1, ..)), None)?;
    token.eval()?;
    let started = Instant::now();
    let mut decode_step: f64 = 0.0;
    let mut tokens = Vec::new();
    for _ in 0..spec.generation_tokens {
        let step = Instant::now();
        let next = token.try_item_cast::<i32>()?;
        tokens.push(next);
        let logits = model.forward(&[next], &mut cache)?;
        token = argmax(logits.index((0, -1, ..)), None)?;
        cache.evaluate_with(&[&token])?;
        decode_step = decode_step.max(step.elapsed().as_secs_f64());
    }
    let decoding = started.elapsed().as_secs_f64();
    let after = usage();
    Ok(json!({
        "prefill_seconds": prefill, "decode_seconds": decoding,
        "prompt_tps": spec.prompt_tokens.len() as f64 / prefill,
        "generation_tps": spec.generation_tokens as f64 / decoding,
        "prefill_step_max_seconds": prefill_step, "decode_step_max_seconds": decode_step,
        "peak_mlx_bytes": memory::peak_memory()?, "live_mlx_bytes": memory::active_memory()?,
        "retained_mlx_bytes": memory::cache_memory()?, "cache_payload_bytes": cache.bytes(),
        "process_peak_rss_bytes": after.ru_maxrss, "major_faults": after.ru_majflt - before.ru_majflt,
        "minor_faults": after.ru_minflt - before.ru_minflt, "block_inputs": after.ru_inblock - before.ru_inblock,
        "tokens": tokens, "ple": "mapped", "kv_bits": 16
    }))
}

fn run() -> Result<()> {
    let args: Vec<_> = std::env::args().collect();
    if args.len() != 3 {
        return Err("Expected model and benchmark input paths".into());
    }
    if let Ok(path) = std::env::var("PW_REFERENCE_METALLIB") {
        mlx_rs::metal::set_metallib_path(path)?;
    }
    let spec: Specification = serde_json::from_slice(&std::fs::read(&args[2])?)?;
    if spec.prompt_tokens.is_empty()
        || spec.prompt_tokens.len() + spec.generation_tokens > 65536
        || !(1..=4096).contains(&spec.generation_tokens)
        || !(1..=20).contains(&spec.trials)
        || !(1..=256).contains(&spec.prefill_chunk)
        || spec.kv_bits != 16
        || spec.ple != "mapped"
    {
        return Err("Unsupported benchmark dimensions".into());
    }
    memory::set_cache_limit(128 << 20)?;
    let started = Instant::now();
    let model = Model::load(Path::new(&args[1]))?;
    let loading = started.elapsed().as_secs_f64();
    let loading_peak = memory::peak_memory()?;
    let loading_rss = usage().ru_maxrss;
    for trial in 0..=spec.trials {
        let mut result = measure(&model, &spec)?;
        result["load_seconds"] = json!(loading);
        result["load_peak_mlx_bytes"] = json!(loading_peak);
        result["load_peak_process_rss_bytes"] = json!(loading_rss);
        result["trial"] = json!(trial);
        result["warmup"] = json!(trial == 0);
        println!("{}", result);
        memory::clear_cache()?;
    }
    Ok(())
}

fn main() {
    if let Err(error) = run() {
        eprintln!("nativegen benchmark: {error}");
        std::process::exit(1);
    }
}
