//! Sampling with an explicit RNG contract so fixtures reproduce exactly.

use crate::error::{Error, Result};

/// xoshiro256**; the seed lives in the request, not in process state.
pub struct Rng {
    s: [u64; 4],
}

impl Rng {
    pub fn new(seed: u64) -> Self {
        // SplitMix64 expansion of the request seed.
        let mut z = seed;
        let mut s = [0u64; 4];
        for slot in &mut s {
            z = z.wrapping_add(0x9E37_79B9_7F4A_7C15);
            let mut x = z;
            x = (x ^ (x >> 30)).wrapping_mul(0xBF58_476D_8CE4_E5B9);
            x = (x ^ (x >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
            *slot = x ^ (x >> 31);
        }
        Self { s }
    }

    fn next_u64(&mut self) -> u64 {
        let r = self.s[0].wrapping_mul(5).wrapping_add(7);
        let t = self.s[1] << 17;
        self.s[2] ^= self.s[0];
        self.s[3] ^= self.s[1];
        self.s[1] ^= self.s[2];
        self.s[0] ^= self.s[3];
        self.s[2] ^= t;
        self.s[3] = self.s[3].rotate_left(45);
        r
    }

    fn next_f32(&mut self) -> f32 {
        (self.next_u64() >> 40) as f32 / (1u64 << 24) as f32
    }
}

/// Sample one token from logits. `top_p == 1.0` means plain temperature sampling.
pub fn sample(logits: &[f32], temperature: f32, top_p: f32, rng: &mut Rng) -> Result<u32> {
    if logits.is_empty() {
        return Err(Error::Capacity("empty logits".into()));
    }
    if temperature <= 0.0 {
        return Ok(argmax(logits));
    }
    let max = logits.iter().cloned().fold(f32::NEG_INFINITY, f32::max);
    let mut probs: Vec<(u32, f32)> = logits
        .iter()
        .enumerate()
        .map(|(i, &l)| (i as u32, (l - max) / temperature))
        .collect();
    for p in &mut probs {
        p.1 = p.1.exp();
    }
    let total: f32 = probs.iter().map(|p| p.1).sum();
    for p in &mut probs {
        p.1 /= total;
    }
    probs.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));

    if top_p < 1.0 {
        // Keep the smallest prefix whose cumulative mass reaches top_p.
        let mut acc = 0.0;
        let cutoff = probs
            .iter()
            .position(|p| {
                acc += p.1;
                acc >= top_p
            })
            .map_or(probs.len(), |i| i + 1);
        probs.truncate(cutoff);
        let rest: f32 = probs.iter().map(|p| p.1).sum();
        for p in &mut probs {
            p.1 /= rest;
        }
    }

    let pick = rng.next_f32();
    let mut acc = 0.0;
    for (token, p) in &probs {
        acc += p;
        if pick < acc {
            return Ok(*token);
        }
    }
    Ok(probs.last().map(|p| p.0).unwrap_or(0))
}

fn argmax(logits: &[f32]) -> u32 {
    let mut best = 0usize;
    for (i, &l) in logits.iter().enumerate() {
        if l > logits[best] {
            best = i;
        }
    }
    best as u32
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn zero_temperature_is_greedy() {
        let mut rng = Rng::new(7);
        assert_eq!(sample(&[0.1, 5.0, 0.2], 0.0, 1.0, &mut rng).unwrap(), 1);
    }

    #[test]
    fn same_seed_same_draws() {
        let a = draws(42);
        let b = draws(42);
        assert_eq!(a, b);
        assert_ne!(a, draws(43));
    }

    fn draws(seed: u64) -> Vec<u32> {
        let mut rng = Rng::new(seed);
        let logits: Vec<f32> = (0..64).map(|i| (i % 7) as f32 * 0.25).collect();
        (0..16).map(|_| sample(&logits, 1.0, 0.9, &mut rng).unwrap()).collect()
    }
}
