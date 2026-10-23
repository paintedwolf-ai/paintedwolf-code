/* Link as a mandatory DT_NEEDED object with -z initfirst -Bsymbolic -z relro -z now. */
#include "syscalls.h"
__attribute__((section(".data.rel.ro"),visibility("default")))
volatile uint64_t pw_module_guard_witness_v2;
/* Matched by the selected auditor, independently of descendant UID mappings. */
__attribute__((visibility("default"),used,aligned(8)))
const unsigned char pw_module_inventory_digest_v1[48]="PW-HASH-SLOT-V1";
static void __attribute__((constructor)) require_auditor(void) {
 if(pw_module_guard_witness_v2!=PW_GUARD_WITNESS)pw_refuse();
}
