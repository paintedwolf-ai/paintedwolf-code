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
                let active = memory::active_memory()?;
                for tile in [0, 8] {
                if tile != 0 && (dimension != 512 || count == 1) { continue; }
                for trial in 0..6 {
                    memory::reset_peak_memory()?;
                    let started = Instant::now();
                    let output = if tile == 0 { fast::scaled_dot_product_attention(
                        &q, &k, &v, 1.0,
                        if count > 1 { Some(fast::ScaledDotProductAttentionMask::Causal) } else { None },
                        None,
                    )? } else {
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
                    println!("{}", json!({"query_tile":tile,"dimension":dimension,"queries":count,"keys":length,
                        "trial":trial,"warmup":trial==0,"seconds":started.elapsed().as_secs_f64(),
                        "active_input_bytes":active,"peak_bytes":memory::peak_memory()?,
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
