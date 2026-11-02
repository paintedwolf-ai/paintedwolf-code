# Vulkan subgroup contract

These crates retain their registry versions and licenses. Their archive identities and the reviewed upstream change are recorded in vulkan-subgroup-provenance.json. The wgpu changes backport draft upstream PR 9523 to 30.0.1; the lib.rs re-export is adapted to this version's layout. Subgroup-size control uses unused feature bit 25 because bit 24 is occupied in this release.

CubeCL kernels select subgroup width from device properties. Vulkan otherwise permits the driver to vary that width, which produced incorrect matrix products on the Arc A310. The candidate adapter requests the advertised maximum width for aligned SPIR-V workgroups and presents that fixed width to kernel selection. Non-Vulkan shader paths retain varying subgroup behavior.

This candidate is not yet qualified. Required evidence: deterministic projection and attention fixtures, same-checkpoint CPU comparisons in F16 and F32, persistent-cache relaunch, and AMD/NVIDIA regression measurements. A diagnostic Mesa SIMD32 override restored the recorded F32 model scores; that override is not part of the application.
