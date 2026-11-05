use mlx_rs::{Array, Dtype, fast, memory, ops, ops::indexing::*, random};
use nativegen::mlx_engine::Result;
use serde_json::json;
use std::time::Instant;

fn input(shape: &[i32], seed: u64) -> Result<Array> {
    let key = random::key(seed)?;
    let array = random::normal::<f32>(shape, None, Some(0.25), &key)?
        .as_dtype(Dtype::Float16)?;
    array.eval()?;
    Ok(array)
}

fn attention(q: &Array, k: &Array, v: &Array) -> Result<Array> {
    let mask = (q.shape()[2] > 1).then_some(fast::ScaledDotProductAttentionMask::Causal);
    Ok(fast::scaled_dot_product_attention(q, k, v, 1.0, mask, None)?)
}

fn reference(q: &Array, k: &Array, v: &Array) -> Result<Array> {
    let count = q.shape()[2];
    let length = k.shape()[2];
    let dimension = q.shape()[3];
    let q = q.reshape(&[1, 2, 4, count, dimension])?;
    let k = k.expand_dims(2)?.swap_axes(-1, -2)?;
    let scores = q.matmul(&k)?;
    let mask: Vec<f32> = (0..count)
        .flat_map(|row| {
            (0..length).map(move |column| {
                if column <= length - count + row { 0.0 } else { -65504.0 }
            })
        })
        .collect();
    let mask = Array::from_slice(&mask, &[count, length]).as_dtype(Dtype::Float16)?;
    let probabilities = ops::softmax_axis(scores.add(&mask)?, -1, true)?;
    Ok(probabilities.matmul(v.expand_dims(2)?)?
        .reshape(&[1, 8, count, dimension])?
        .as_dtype(Dtype::Float32)?)
}

fn compare(output: &Array, expected: &Array) -> Result<f32> {
    let actual = output.as_dtype(Dtype::Float32)?.contiguous()?;
    actual.eval()?;
    let mut maximum = 0.0_f32;
    for (&got, &want) in actual.as_slice::<f32>().iter().zip(expected.as_slice::<f32>()) {
        if !got.is_finite() || !want.is_finite() {
            return Err("non-finite attention output".into());
        }
        maximum = maximum.max((got - want).abs());
    }
    if maximum > 0.005 {
        return Err(format!("attention parity failed: {maximum}").into());
    }
    Ok(maximum)
}

fn measure(dimension: i32, count: i32, length: i32) -> Result<()> {
    let q = input(&[1, count, 8, dimension], 1)?.transpose_axes(&[0, 2, 1, 3])?;
    let keys = input(&[1, 2, 49152, dimension], 2)?;
    let values = input(&[1, 2, 49152, dimension], 3)?;
    let k = keys.index((.., .., ..length, ..));
    let v = values.index((.., .., ..length, ..));
    let expected = reference(&q, &k, &v)?;
    expected.eval()?;
    if unsafe { mlx_sys::mlx_synchronize(mlx_rs::Stream::gpu().as_ptr()) } != 0 {
        return Err("attention probe synchronization failed".into());
    }
    for trial in 0..6 {
        memory::reset_peak_memory()?;
        let started = Instant::now();
        let output = attention(&q, &k, &v)?;
        output.eval()?;
        let seconds = started.elapsed().as_secs_f64();
        let peak = memory::peak_memory()?;
        let error = compare(&output, &expected)?;
        println!("{}", json!({
            "dimension": dimension, "queries": count, "keys": length,
            "trial": trial, "warmup": trial == 0, "seconds": seconds,
            "max_absolute_error": error, "peak_bytes": peak,
            "q_strides": q.strides(), "k_strides": k.strides(),
            "d512_min_kl": std::env::var("MLX_SDPA_D512_MIN_KL").ok()
        }));
    }
    Ok(())
}

pub fn run() -> Result<()> {
    memory::set_cache_limit(128 << 20)?;
    for dimension in [256, 512] {
        for count in [1, 128] {
            for length in [1024, 4095, 4096, 4097, 8192, 35269] {
                measure(dimension, count, length)?;
                memory::clear_cache()?;
            }
        }
    }
    Ok(())
}

#[cfg(test)]
#[path = "attention_probe_tests.rs"]
mod tests;
