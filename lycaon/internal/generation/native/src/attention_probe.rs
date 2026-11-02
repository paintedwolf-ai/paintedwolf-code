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
    if q.shape()[3] == 512 && q.shape()[2] > 8
        && std::env::var("PW_MLX_E4B_D512").as_deref() == Ok("1")
    {
        let empty = unsafe { Array::from_ptr(mlx_sys::mlx_array_new()) };
        let mut output = unsafe { mlx_sys::mlx_array_new() };
        let status = unsafe {
            mlx_sys::mlx_fast_scaled_dot_product_attention(
                &mut output, q.as_ptr(), k.as_ptr(), v.as_ptr(), 1.0,
                c"causal".as_ptr(), empty.as_ptr(), empty.as_ptr(), true,
                mlx_rs::Stream::gpu().as_ptr(),
            )
        };
        if status != 0 {
            unsafe { mlx_sys::mlx_array_free(output) };
            return Err("fused attention rejected the probe geometry".into());
        }
        return Ok(unsafe { Array::from_ptr(output) });
    }
    Ok(fast::scaled_dot_product_attention(q, k, v, 1.0,
        (q.shape()[2] > 1).then_some(fast::ScaledDotProductAttentionMask::Causal), None)?)
}

fn reference(q: &Array, k: &Array, v: &Array) -> Result<Array> {
    let count = q.shape()[2];
    let length = k.shape()[2];
    let dimension = q.shape()[3];
    let q = q.reshape(&[1, 2, 4, count, dimension])?;
    let k = k.expand_dims(2)?.swap_axes(-1, -2)?;
    let scores = q.matmul(&k)?;
    let mask: Vec<f32> = (0..count).flat_map(|row| {
        (0..length).map(move |column| if column <= length - count + row { 0.0 } else { -65504.0 })
    }).collect();
    let mask = Array::from_slice(&mask, &[count, length]).as_dtype(Dtype::Float16)?;
    let probabilities = ops::softmax_axis(scores.add(&mask)?, -1, true)?;
    Ok(probabilities.matmul(v.expand_dims(2)?)?.reshape(&[1, 8, count, dimension])?
        .as_dtype(Dtype::Float32)?)
}

pub fn run() -> Result<()> {
    memory::set_cache_limit(128 << 20)?;
    for dimension in [256, 512] {
        for count in [1, 128] {
            for length in [1024, 8192, 35269] {
                let q = input(&[1, count, 8, dimension], 1)?.transpose_axes(&[0, 2, 1, 3])?;
                let keys = input(&[1, 2, 49152, dimension], 2)?;
                let values = input(&[1, 2, 49152, dimension], 3)?;
                let k = keys.index((.., .., ..length, ..));
                let v = values.index((.., .., ..length, ..));
                k.eval()?;
                v.eval()?;
                let expected = reference(&q, &k, &v)?;
                expected.eval()?;
                let active = memory::active_memory()?;
                for tile in [0, 8] {
                if tile != 0 && (dimension != 512 || count == 1) { continue; }
                for trial in 0..6 {
                    memory::reset_peak_memory()?;
                    let started = Instant::now();
                    let output = if tile == 0 { attention(&q, &k, &v)? } else {
                        let mut pieces = Vec::new();
                        for offset in (0..count).step_by(tile as usize) {
                            let end = (offset + tile).min(count);
                            let visible = length - count + end;
                            pieces.push(fast::scaled_dot_product_attention(
                                q.index((.., .., offset..end, ..)),
                                k.index((.., .., ..visible, ..)),
                                v.index((.., .., ..visible, ..)), 1.0,
                                fast::ScaledDotProductAttentionMask::Causal, None)?);
                        }
                        ops::concatenate(&pieces.iter().collect::<Vec<_>>(), 2)?
                    };
                    output.eval()?;
                    let seconds = started.elapsed().as_secs_f64();
                    let peak = memory::peak_memory()?;
                    let actual = output.as_dtype(Dtype::Float32)?.contiguous()?;
                    actual.eval()?;
                    if actual.as_slice::<f32>().iter().chain(expected.as_slice::<f32>()).any(|x| !x.is_finite()) {
                        return Err("non-finite attention output".into());
                    }
                    let maximum_error = actual.as_slice::<f32>().iter()
                        .zip(expected.as_slice::<f32>()).map(|(a, b)| (a - b).abs())
                        .fold(0.0_f32, f32::max);
                    if !maximum_error.is_finite() || maximum_error > 0.005 {
                        return Err(format!("attention parity failed: {maximum_error}").into());
                    }
                    println!("{}", json!({"query_tile":tile,"dimension":dimension,"queries":count,"keys":length,
                        "trial":trial,"warmup":trial==0,"seconds":seconds,"max_absolute_error":maximum_error,
                        "active_input_bytes":active,"peak_bytes":peak,"e4b_d512":std::env::var("PW_MLX_E4B_D512").ok(),
                        "q_strides":q.strides(),"k_strides":k.strides(),
                        "d512_min_kl":std::env::var("MLX_SDPA_D512_MIN_KL").ok()}));
                }
                }
                memory::clear_cache()?;
            }
        }
    }
    Ok(())
}
