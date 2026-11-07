#pragma once
#include <cstdint>
struct pw_inventory { uint64_t resident; uint64_t mapped; };
pw_inventory inspect_e4b(const char * path);
