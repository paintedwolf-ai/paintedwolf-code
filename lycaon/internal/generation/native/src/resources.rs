use crate::error::{Error, Result};
use serde::Serialize;

#[derive(Serialize)]
pub struct Plan {
    pub resident_weights_bytes: u64,
    pub mapped_ple_bytes: u64,
    pub kv_capacity_bytes: u64,
    pub workspace_bytes: u64,
    pub allocator_cache_bytes: u64,
    pub device_reservation_bytes: u64,
    pub host_reservation_bytes: u64,
}

impl Plan {
    pub fn e4b(
        weights: u64,
        mapped: u64,
        context: usize,
        chunk: usize,
        unified: bool,
    ) -> Result<Self> {
        if !(2..=65536).contains(&context)
            || !(1..=256).contains(&chunk)
            || weights == 0
            || weights > 8 << 30
            || mapped == 0
            || mapped > 4 << 30
        {
            return Err(Error::Capacity("invalid E4B resource dimensions".into()));
        }
        let context = (context as u64).div_ceil(256) * 256;
        // Four global and twenty sliding KV sources; keys and values use F16.
        let global = context * 4 * 2 * 2 * 512 * 2;
        let sliding_tokens = if unified {
            2 * (512 + chunk as u64)
        } else {
            (1024 + chunk as u64).div_ceil(256) * 256
        };
        let sliding = sliding_tokens * 20 * 2 * 2 * 256 * 2;
        let kv_capacity_bytes = global + sliding;
        let workspace_bytes = if unified {
            (384 << 20).max(chunk as u64 * (3 << 20))
        } else {
            768 << 20
        };
        let allocator_cache_bytes = if unified { 128 << 20 } else { 0 };
        let device_reservation_bytes =
            weights + kv_capacity_bytes + workspace_bytes + allocator_cache_bytes;
        // Tokenizer, grammar compilation, IPC and mapped rows share host memory.
        let host_reservation_bytes = (if unified { 512 << 20 } else { 768 << 20 })
            + crate::output_contract::HOST_BYTES;
        Ok(Self {
            resident_weights_bytes: weights,
            mapped_ple_bytes: mapped,
            kv_capacity_bytes,
            workspace_bytes,
            allocator_cache_bytes,
            device_reservation_bytes,
            host_reservation_bytes,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn mlx_workspace_scales_with_chunk_without_cache_growth() {
        let small = Plan::e4b(2787934208, 2290089984, 8192, 128, true).unwrap();
        let large = Plan::e4b(2787934208, 2290089984, 8192, 256, true).unwrap();
        assert_eq!(small.workspace_bytes, 384 << 20);
        assert_eq!(large.workspace_bytes, 768 << 20);
        assert_eq!(small.host_reservation_bytes, (512 << 20) + crate::output_contract::HOST_BYTES);
        assert!(
            small.device_reservation_bytes + small.host_reservation_bytes + (2 << 30) < 6 << 30
        );
        let tiny = Plan::e4b(2787934208, 2290089984, 8192, 1, true).unwrap();
        assert_eq!(tiny.workspace_bytes, small.workspace_bytes);
    }

    #[test]
    fn vulkan_sliding_capacity_includes_padded_active_and_retained_windows() {
        let global = 8192 * 4 * 2 * 2 * 512 * 2;
        for chunk in [1, 16, 128, 256] {
            let plan = Plan::e4b(3 << 30, 2 << 30, 8192, chunk, false).unwrap();
            assert_eq!(plan.kv_capacity_bytes - global, 1280 * 20 * 2 * 2 * 256 * 2);
        }
    }

    #[test]
    fn accounts_for_backend_cache_residency_without_charging_the_entire_mapping() {
        let linux = Plan::e4b(3 << 30, 2 << 30, 65536, 256, false).unwrap();
        let mac = Plan::e4b(3 << 30, 2 << 30, 65536, 256, true).unwrap();
        assert_eq!(mac.kv_capacity_bytes - linux.kv_capacity_bytes, 10 << 20);
        assert_eq!(
            mac.device_reservation_bytes - linux.device_reservation_bytes,
            138 << 20
        );
        assert!(linux.host_reservation_bytes < linux.mapped_ple_bytes);
        assert!(linux.device_reservation_bytes < 6 << 30);
        for (context, chunk) in [(0, 1), (65537, 1), (2, 0), (2, 257)] {
            assert!(Plan::e4b(3 << 30, 2 << 30, context, chunk, false).is_err());
        }
    }
}
