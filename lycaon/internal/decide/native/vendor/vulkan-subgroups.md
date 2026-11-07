# Vulkan subgroup contract

These crates retain their registry versions and licenses. Their archive identities and the reviewed upstream change are recorded in vulkan-subgroup-provenance.json. The wgpu changes backport draft upstream PR 9523 to 30.0.1; the lib.rs re-export is adapted to this version's layout. Subgroup-size control uses unused feature bit 25 because bit 24 is occupied in this release.

CubeCL kernels select subgroup width from device properties. Vulkan otherwise permits the driver to vary that width, which produced incorrect matrix products on the Arc A310. The adapter requests the advertised maximum width for aligned SPIR-V workgroups and presents that fixed width to kernel selection. Non-Vulkan shader paths retain varying subgroup behavior.

Qualification covers 81 native fixtures on Arc A310 with Mesa 26.2.5, same-checkpoint Intel/AMD F32 equality at protocol precision, and Intel/AMD/NVIDIA F16 rank ordering. All sampled scores are stable across persistent-cache relaunches. Maximum sampled probability differences from CPU are 0.0045, 0.0078 and 0.0064 respectively. These small fixtures do not establish trained-model quality or every device configuration. No Mesa subgroup override is part of the application.
