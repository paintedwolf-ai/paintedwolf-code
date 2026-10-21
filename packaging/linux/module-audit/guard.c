/* Link as a mandatory DT_NEEDED object with -z initfirst -Bsymbolic -z relro -z now. */
#include "syscalls.h"
__attribute__((section(".data.rel.ro"),visibility("default")))
volatile uint64_t pw_module_guard_witness_v2;
static void __attribute__((constructor)) require_auditor(void) {
 if(pw_module_guard_witness_v2!=PW_GUARD_WITNESS)pw_refuse();
}
