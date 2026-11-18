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
    pub fn for_model(
        profile: crate::profile::Profile,
        weights: u64,
        mapped: u64,
        context: usize,
        chunk: usize,
        unified: bool,
    ) -> Result<Self> {
        if !(2..=65536).contains(&context)
            || !(1..=256).contains(&chunk)
            || weights == 0
            || weights > profile.weight_limit
            || (mapped != 0) != profile.has_ple()
            || mapped > 4 << 30
        {
            return Err(Error::Capacity("invalid model resource dimensions".into()));
        }
        let context = (context as u64).div_ceil(256) * 256;
        let global_layers = profile.kv_layers() as u64 / 6;
        let sliding_layers = profile.kv_layers() as u64 - global_layers;
        let global = context * global_layers * profile.global_heads as u64 * 512 * 4;
        let sliding_tokens = if unified {
            2 * (profile.window as u64 + chunk as u64)
        } else {
            (2 * profile.window as u64 + chunk as u64).div_ceil(256) * 256
        };
        let sliding = sliding_tokens * sliding_layers * profile.sliding_heads as u64 * 256 * 4;
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
        let host_reservation_bytes =
            (if unified { 512 << 20 } else { 768 << 20 }) + crate::output_contract::HOST_BYTES;
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
    fn non_ple_profiles_reserve_their_own_kv_heads_and_layer_counts() {
        for (profile, global, sliding, heads) in [
            (crate::profile::DENSE, 8, 40, 1),
            (crate::profile::MOE, 5, 25, 2),
        ] {
            let plan = Plan::for_model(profile, profile.weight_limit, 0, 8192, 256, true).unwrap();
            assert_eq!(
                plan.kv_capacity_bytes,
                global * heads * 512 * 8192 * 4 + sliding * 8 * 256 * 2 * (1024 + 256) * 4
            );
            assert!(
                Plan::for_model(profile, profile.weight_limit + 1, 0, 8192, 256, true).is_err()
            );
            assert!(Plan::for_model(profile, profile.weight_limit, 4, 8192, 256, true).is_err());
        }
    }
    #[test]
    fn mlx_workspace_scales_with_chunk_without_cache_growth() {
        let small =
            Plan::for_model(crate::profile::E4B, 2787934208, 2290089984, 8192, 128, true).unwrap();
        let large =
            Plan::for_model(crate::profile::E4B, 2787934208, 2290089984, 8192, 256, true).unwrap();
        assert_eq!(small.workspace_bytes, 384 << 20);
        assert_eq!(large.workspace_bytes, 768 << 20);
        assert_eq!(
            small.host_reservation_bytes,
            (512 << 20) + crate::output_contract::HOST_BYTES
        );
        assert!(
            small.device_reservation_bytes + small.host_reservation_bytes + (2 << 30) < 6 << 30
        );
        let tiny =
            Plan::for_model(crate::profile::E4B, 2787934208, 2290089984, 8192, 1, true).unwrap();
        assert_eq!(tiny.workspace_bytes, small.workspace_bytes);
    }

    #[test]
    fn vulkan_sliding_capacity_includes_padded_active_and_retained_windows() {
        let global = 8192 * 4 * 2 * 2 * 512 * 2;
        for chunk in [1, 16, 128, 256] {
            let plan =
                Plan::for_model(crate::profile::E4B, 3 << 30, 2 << 30, 8192, chunk, false).unwrap();
            assert_eq!(plan.kv_capacity_bytes - global, 1280 * 20 * 2 * 2 * 256 * 2);
        }
    }

    #[test]
    fn accounts_for_backend_cache_residency_without_charging_the_entire_mapping() {
        let linux =
            Plan::for_model(crate::profile::E4B, 3 << 30, 2 << 30, 65536, 256, false).unwrap();
        let mac = Plan::for_model(crate::profile::E4B, 3 << 30, 2 << 30, 65536, 256, true).unwrap();
        assert_eq!(mac.kv_capacity_bytes - linux.kv_capacity_bytes, 10 << 20);
        assert_eq!(
            mac.device_reservation_bytes - linux.device_reservation_bytes,
            138 << 20
        );
        assert!(linux.host_reservation_bytes < linux.mapped_ple_bytes);
        assert!(linux.device_reservation_bytes < 6 << 30);
        for (context, chunk) in [(0, 1), (65537, 1), (2, 0), (2, 257)] {
            assert!(
                Plan::for_model(crate::profile::E4B, 3 << 30, 2 << 30, context, chunk, false)
                    .is_err()
            );
        }
    }
}
