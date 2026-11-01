#pragma once
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif
struct pw_generation;
struct pw_device_info {
    uint64_t total_bytes;
    uint64_t free_bytes;
};
int pw_generation_inspect(const char * model, uint64_t * resident, uint64_t * mapped, char * error, size_t capacity);
int pw_generation_probe(const char * pci, struct pw_device_info *, char * error, size_t capacity);
int pw_generation_load(const char * model, const char * pci, uint32_t context,
                       uint32_t chunk, uint32_t prefix, uint64_t budget, struct pw_generation **,
                       char * error, size_t capacity);
int pw_generation_forward(struct pw_generation *, const uint32_t * tokens, size_t count,
                          int output, float * logits, uint32_t * sampled, char * error, size_t capacity);
int pw_generation_cache(struct pw_generation *, int operation, int * restored, char * error, size_t capacity);
void pw_generation_free(struct pw_generation *);
#ifdef __cplusplus
}
#endif
