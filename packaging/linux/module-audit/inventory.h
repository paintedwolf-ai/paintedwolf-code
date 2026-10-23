#ifndef PW_MODULE_INVENTORY_H
#define PW_MODULE_INVENTORY_H
#include "policy.h"
#define PW_INVENTORY_CAPACITY (4U*1024U*1024U)
#define PW_INVENTORY_ENTRIES 32768U
/* The producer fills this read-only slot before the generation's final seal. */
__attribute__((visibility("default"),used,aligned(8)))
const unsigned char pw_module_inventory_v1[PW_INVENTORY_CAPACITY]="PW-INV-SLOT-V1\0";
__attribute__((visibility("default"),used,aligned(8)))
const unsigned char pw_module_guard_digest_v1[48]="PW-GUARD-HASH-V1";
extern int pw_sha256_bytes(const unsigned char *,size_t,unsigned char *);
extern int pw_sha256_fd(int,uint64_t,unsigned char *);
static unsigned char pw_inventory[PW_INVENTORY_CAPACITY],pw_inventory_digest[32];
static uint32_t pw_inventory_offsets[PW_INVENTORY_ENTRIES],pw_inventory_count;
static uint32_t pw_inventory_size;
static uint64_t pw_guard_inventory_offset;
static int pw_inventory_ready;
static uint16_t pw_u16(const unsigned char *p) {return p[0]|((uint16_t)p[1]<<8);}
static uint64_t pw_u64(const unsigned char *p) {return pw_u32(p)|((uint64_t)pw_u32(p+4)<<32);}
static int pw_order(const char *a,size_t an,const char *b,size_t bn) {
 size_t n=an<bn?an:bn;
 for(size_t i=0;i<n;++i)if((unsigned char)a[i]!=(unsigned char)b[i])return (unsigned char)a[i]<(unsigned char)b[i]?-1:1;
 return an==bn?0:(an<bn?-1:1);
}
static int pw_inventory_path(const char *path,size_t n) {
 if(!pw_canonical(path,n))return 0;
 /* Only canonical UTF-8 encodings enter the producer's alias namespace. */
 for(size_t i=0;i<n;) {
  unsigned a=(unsigned char)path[i++],remaining=0,minimum=0;
  if(a<128)continue;
  if(a>=0xc2&&a<=0xdf){remaining=1;minimum=0x80;a&=0x1f;}
  else if(a>=0xe0&&a<=0xef){remaining=2;minimum=0x800;a&=0x0f;}
  else if(a>=0xf0&&a<=0xf4){remaining=3;minimum=0x10000;a&=7;}
  else return 0;
  if(remaining>n-i)return 0;
  while(remaining--){unsigned b=(unsigned char)path[i++];if((b&0xc0)!=0x80)return 0;a=(a<<6)|(b&0x3f);}
  if(a<minimum||a>0x10ffff||(a>=0xd800&&a<=0xdfff))return 0;
 }
 return 1;
}
static int pw_load_inventory(void) {
 const volatile unsigned char *source=pw_module_inventory_v1;
 for(size_t i=0;i<40;++i)pw_inventory[i]=source[16+i];
 int empty=1;for(size_t i=0;i<40;++i)if(pw_inventory[i])empty=0;
 if(empty)return 0; /* Unbound source fixtures still require the root-sealed policy. */
 static const char marker[16]="PW-INV-SLOT-V1\0";
 for(size_t i=0;i<16;++i)if(source[i]!=(unsigned char)marker[i])pw_refuse();
 pw_inventory_size=pw_u32(pw_inventory+8);pw_inventory_count=pw_u32(pw_inventory+12);
 pw_guard_offset=pw_u64(pw_inventory+16);pw_guard_inventory_offset=pw_u64(pw_inventory+24);
 pw_guard_size=pw_u16(pw_inventory+32);
#if defined(__x86_64__)
 const unsigned machine=62;
#else
 const unsigned machine=183;
#endif
 if(!pw_equal((char *)pw_inventory,"PWMIV01\0",8)||pw_inventory_size<40||pw_inventory_size>PW_INVENTORY_CAPACITY-16
    ||pw_inventory_count==0||pw_inventory_count>PW_INVENTORY_ENTRIES||pw_u16(pw_inventory+34)!=machine
    ||pw_u32(pw_inventory+36)||!pw_guard_offset||pw_guard_offset%8||pw_guard_offset>64*1024*1024
    ||!pw_guard_inventory_offset||pw_guard_inventory_offset%8||pw_guard_inventory_offset>64*1024*1024)pw_refuse();
 for(size_t i=40;i<pw_inventory_size;++i)pw_inventory[i]=source[16+i];
 if(pw_guard_size>pw_inventory_size-40||!pw_inventory_path((char *)pw_inventory+40,pw_guard_size))pw_refuse();
 pw_guard_path=(char *)pw_inventory+40;
 size_t cursor=40+pw_guard_size,previous=0;
 for(uint32_t i=0;i<pw_inventory_count;++i) {
  if(pw_inventory_size-cursor<42)pw_refuse();
  uint64_t size=pw_u64(pw_inventory+cursor);uint16_t n=pw_u16(pw_inventory+cursor+40);
  if(size==0||size>=UINT64_C(1)<<63||n>pw_inventory_size-cursor-42
     ||!pw_inventory_path((char *)pw_inventory+cursor+42,n)
     ||(n==pw_guard_size&&pw_equal((char *)pw_inventory+cursor+42,pw_guard_path,n)))pw_refuse();
  if(i&&pw_order((char *)pw_inventory+previous+42,pw_u16(pw_inventory+previous+40),
                 (char *)pw_inventory+cursor+42,n)>=0)pw_refuse();
  pw_inventory_offsets[i]=(uint32_t)cursor;previous=cursor;cursor+=42+n;
 }
 if(cursor!=pw_inventory_size||pw_sha256_bytes(pw_inventory,pw_inventory_size,pw_inventory_digest))pw_refuse();
 pw_inventory_ready=1;pw_policy_ready=1;return 1;
}
static int pw_inventory_module(int fd,const char *path,size_t n,const struct statx *facts) {
 if(n==pw_guard_size&&pw_equal(path,pw_guard_path,n)) {
  const volatile unsigned char *selected=pw_module_guard_digest_v1;
  unsigned char digest[32];
  for(size_t i=0;i<16;++i)if(selected[i]!=(unsigned char)"PW-GUARD-HASH-V1"[i])return 0;
  if(pw_sha256_fd(fd,facts->stx_size,digest))return 0;
  for(size_t i=0;i<32;++i)if(digest[i]!=selected[16+i])return 0;
  return 1;
 }
 uint32_t first=0,last=pw_inventory_count;
 while(first<last) {
  uint32_t mid=first+(last-first)/2,offset=pw_inventory_offsets[mid];
  const unsigned char *entry=pw_inventory+offset;
  int order=pw_order(path,n,(char *)entry+42,pw_u16(entry+40));
  if(order<0)last=mid;
  else if(order>0)first=mid+1;
  else {
   unsigned char digest[32];
   return pw_stat_complete(facts)&&facts->stx_size==pw_u64(entry)
    &&!pw_sha256_fd(fd,facts->stx_size,digest)&&pw_equal((char *)digest,(char *)entry+8,32);
  }
 }
 return 0;
}
#endif
