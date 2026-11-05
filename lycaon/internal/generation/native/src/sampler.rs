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
        let r = self.s[1].wrapping_mul(5).rotate_left(7).wrapping_mul(9);
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
pub fn sample(
    logits: &[f32],
    temperature: f32,
    top_p: f32,
    top_k: usize,
    rng: &mut Rng,
) -> Result<u32> {
    if logits.is_empty() || logits.len() > 262144 || top_k > 262144 {
        return Err(Error::Capacity("logit count exceeds its bounds".into()));
    }
    if !temperature.is_finite()
        || temperature < 0.0
        || !top_p.is_finite()
        || !(0.0 < top_p && top_p <= 1.0)
    {
        return Err(Error::Protocol("invalid sampling parameters".into()));
    }
    if logits.iter().any(|l| l.is_nan() || *l == f32::INFINITY)
        || !logits.iter().any(|l| l.is_finite())
    {
        return Err(Error::Device("invalid model logits".into()));
    }
    if temperature == 0.0 {
        return Ok(argmax(logits));
    }
    let max = logits.iter().cloned().fold(f32::NEG_INFINITY, f32::max);
    let mut probs: Vec<(u32, f32)> = logits
        .iter()
        .enumerate()
        .map(|(i, &l)| (i as u32, (l - max) / temperature))
        .collect();
    if top_k > 0 && top_k < probs.len() {
        probs.select_nth_unstable_by(top_k - 1, |a, b| b.1.total_cmp(&a.1).then(a.0.cmp(&b.0)));
        probs.truncate(top_k);
    }
    for p in &mut probs {
        p.1 = p.1.exp();
    }
    let mut total: f64 = probs.iter().map(|p| f64::from(p.1)).sum();
    if top_p < 1.0 {
        probs.sort_by(|a, b| b.1.total_cmp(&a.1).then(a.0.cmp(&b.0)));
        let threshold = f64::from(top_p) * total;
        let mut acc = 0.0;
        let cutoff = probs
            .iter()
            .position(|p| {
                acc += f64::from(p.1);
                acc >= threshold
            })
            .map_or(probs.len(), |i| i + 1);
        probs.truncate(cutoff);
        total = probs.iter().map(|p| f64::from(p.1)).sum();
    }

    let pick = f64::from(rng.next_f32()) * total;
    let mut acc = 0.0;
    let mut last_supported = 0;
    for (token, p) in &probs {
        if *p == 0.0 {
            continue;
        }
        last_supported = *token;
        acc += f64::from(*p);
        if pick < acc {
            return Ok(*token);
        }
    }
    // Rounding must never select a masked or underflowed token.
    Ok(last_supported)
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
    fn xoshiro_reference_state_sequence() {
        let mut rng = Rng { s: [1, 2, 3, 4] };
        assert_eq!(rng.next_u64(), 11520);
        assert_eq!(rng.next_u64(), 0);
        assert_eq!(rng.next_u64(), 1509978240);
    }

    #[test]
    fn refuses_invalid_distributions_and_parameters() {
        let mut rng = Rng::new(7);
        for logits in [
            vec![],
            vec![f32::NAN],
            vec![f32::INFINITY],
            vec![f32::NEG_INFINITY],
        ] {
            assert!(sample(&logits, 1.0, 1.0, 0, &mut rng).is_err());
        }
        for (temperature, top_p) in [
            (f32::NAN, 1.0),
            (-1.0, 1.0),
            (1.0, 0.0),
            (1.0, 1.1),
            (1.0, f32::NAN),
        ] {
            assert!(sample(&[1.0], temperature, top_p, 0, &mut rng).is_err());
        }
        assert_eq!(
            sample(&[f32::NEG_INFINITY, 2.0], 1.0, 1.0, 0, &mut rng).unwrap(),
            1
        );
    }

    #[test]
    fn zero_temperature_is_greedy() {
        let mut rng = Rng::new(7);
        assert_eq!(sample(&[0.1, 5.0, 0.2], 0.0, 1.0, 0, &mut rng).unwrap(), 1);
    }

    #[test]
    fn top_k_excludes_lower_logits_and_breaks_ties_by_token() {
        let mut rng = Rng::new(4);
        for _ in 0..100 {
            assert_eq!(sample(&[3.0, 3.0, 2.0], 1.0, 1.0, 1, &mut rng).unwrap(), 0);
            assert!(matches!(
                sample(&[0.0, 3.0, 4.0, 2.0], 1.0, 0.95, 2, &mut rng).unwrap(),
                1 | 2
            ));
        }
    }

    #[test]
    fn final_draw_stays_inside_distribution_support() {
        for top_p in [1.0, 0.9999] {
            let mut logits = vec![0.0; 1000];
            logits.extend([f32::NEG_INFINITY, -1000.0]);
            // The first draw is the largest possible value below one.
            let mut rng = Rng {
                s: [0, 0x4fc7_1c71_c71c_71c7, 0, 0],
            };
            assert_eq!(sample(&logits, 1.0, top_p, 0, &mut rng).unwrap(), 999);
        }
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
        (0..16)
            .map(|_| sample(&logits, 1.0, 0.9, 0, &mut rng).unwrap())
            .collect()
    }
}
