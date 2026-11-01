use mlx_rs::{memory, ops::indexing::*};
use nativegen::mlx_engine::{Cache, Logits, Model, Result};
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
    #[serde(default)]
    verify_last_logits: bool,
}

fn compare_logits(model: &Model, spec: &Specification) -> Result<()> {
    let mut full_cache = Cache::new(spec.prompt_tokens.len() + spec.generation_tokens)?;
    let mut last_cache = Cache::new(spec.prompt_tokens.len() + spec.generation_tokens)?;
    let mut prefill_cache = Cache::new(spec.prompt_tokens.len() + spec.generation_tokens)?;
    for (index, chunk) in spec.prompt_tokens.chunks(spec.prefill_chunk).enumerate() {
        let full = model
            .forward(chunk, &mut full_cache, Logits::All)?
            .ok_or("missing full logits")?;
        let last = model
            .forward(chunk, &mut last_cache, Logits::Last)?
            .ok_or("missing last logits")?;
        if model
            .forward(chunk, &mut prefill_cache, Logits::None)?
            .is_some()
        {
            return Err("cache-only prefill returned logits".into());
        }
        prefill_cache.evaluate()?;
        let full = full.index((0, -1, ..)).as_dtype(mlx_rs::Dtype::Float32)?;
        let last = last.index((0, -1, ..)).as_dtype(mlx_rs::Dtype::Float32)?;
        full_cache.evaluate_with(&[&full])?;
        last_cache.evaluate_with(&[&last])?;
        let mut maximum = 0_f32;
        for (&a, &b) in full.as_slice::<f32>().iter().zip(last.as_slice::<f32>()) {
            if !a.is_finite() || !b.is_finite() {
                return Err("non-finite parity logits".into());
            }
            maximum = maximum.max((a - b).abs());
        }
        let full_token = argmax(&full, None)?.try_item_cast::<i32>()?;
        let last_token = argmax(&last, None)?.try_item_cast::<i32>()?;
        let (variation, divergence) =
            distribution_distance(full.as_slice::<f32>(), last.as_slice::<f32>());
        println!(
            "{}",
            json!({"parity_chunk": index, "tokens": chunk.len(),
            "max_absolute_logit_difference": maximum, "full_token": full_token,
            "last_token": last_token, "cache_bytes": last_cache.bytes(),
            "distribution_total_variation": variation, "kl_divergence_nats": divergence})
        );
        if full_token != last_token
            || full_cache.bytes() != last_cache.bytes()
            || full_cache.bytes() != prefill_cache.bytes()
        {
            return Err("last-row projection changed greedy output or cache storage".into());
        }
    }
    let mut token = *spec.prompt_tokens.last().ok_or("empty parity input")?;
    for step in 0..spec.generation_tokens.min(4) {
        let full = model
            .forward(&[token], &mut full_cache, Logits::Last)?
            .ok_or("missing full-cache logits")?;
        let pruned = model
            .forward(&[token], &mut prefill_cache, Logits::Last)?
            .ok_or("missing pruned-cache logits")?;
        full_cache.evaluate_with(&[&full])?;
        prefill_cache.evaluate_with(&[&pruned])?;
        let full = full.as_dtype(mlx_rs::Dtype::Float32)?;
        let pruned = pruned.as_dtype(mlx_rs::Dtype::Float32)?;
        let equal = full.as_slice::<f32>() == pruned.as_slice::<f32>();
        if !equal || full_cache.bytes() != prefill_cache.bytes() {
            return Err("cache-only prefill changed subsequent logits or cache storage".into());
        }
        token = argmax(full.index((0, -1, ..)), None)?.try_item_cast::<i32>()?;
        println!(
            "{}",
            json!({"cache_only_parity_step":step,"exact_logits":true,"token":token})
        );
    }
    Ok(())
}

fn distribution_distance(full: &[f32], last: &[f32]) -> (f64, f64) {
    let probabilities = |logits: &[f32]| {
        let maximum = logits.iter().copied().fold(f32::NEG_INFINITY, f32::max);
        let weights: Vec<f64> = logits
            .iter()
            .map(|value| f64::from(*value - maximum).exp())
            .collect();
        let total: f64 = weights.iter().sum();
        weights
            .into_iter()
            .map(|weight| weight / total)
            .collect::<Vec<_>>()
    };
    probabilities(full)
        .into_iter()
        .zip(probabilities(last))
        .fold((0.0, 0.0), |(variation, divergence), (p, q)| {
            (
                variation + (p - q).abs() / 2.0,
                divergence + p * (p / q).ln(),
            )
        })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn distribution_comparison_is_invariant_to_logit_offsets() {
        let (variation, divergence) = distribution_distance(&[1.0, 2.0, 3.0], &[2.0, 3.0, 4.0]);
        assert_eq!((variation, divergence), (0.0, 0.0));
        let (variation, divergence) = distribution_distance(&[0.0, 0.0], &[0.0, 3_f32.ln()]);
        assert!((variation - 0.25).abs() < 1e-7);
        assert!((divergence - (4_f64 / 3.0).ln() / 2.0).abs() < 1e-7);
    }
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
    let mut cache = Cache::new(spec.prompt_tokens.len() + spec.generation_tokens)?;
    memory::reset_peak_memory()?;
    let before = usage();
    let started = Instant::now();
    let mut prefill_step: f64 = 0.0;
    for chunk in spec.prompt_tokens[..spec.prompt_tokens.len() - 1].chunks(spec.prefill_chunk) {
        let step = Instant::now();
        let _ = model.forward(chunk, &mut cache, Logits::None)?;
        cache.evaluate()?;
        prefill_step = prefill_step.max(step.elapsed().as_secs_f64());
    }
    let step = Instant::now();
    let logits = model
        .forward(
            &spec.prompt_tokens[spec.prompt_tokens.len() - 1..],
            &mut cache,
            Logits::Last,
        )?
        .ok_or("missing prompt logits")?;
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
        let logits = model
            .forward(&[next], &mut cache, Logits::Last)?
            .ok_or("missing decode logits")?;
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
    if spec.verify_last_logits {
        compare_logits(&model, &spec)?;
        memory::clear_cache()?;
    }
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
