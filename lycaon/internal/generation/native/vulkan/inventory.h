#pragma once
#include <stddef.h>
#include <stdint.h>

struct pw_gpu_inventory {
    char pci[32];
    char name[256];
    uint64_t total_bytes;
    uint64_t available_bytes;
    uint32_t unified_memory;
    uint32_t availability_known;
};
extern "C" int pw_generation_inventory(pw_gpu_inventory * devices, size_t capacity,
                                        size_t * count, char * error, size_t error_capacity);
