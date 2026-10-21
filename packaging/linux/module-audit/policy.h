#ifndef PW_MODULE_POLICY_H
#define PW_MODULE_POLICY_H
#include "syscalls.h"
#define PW_POLICY_FD 7
#define PW_POLICY_LIMIT 65536
#define PW_ROOT_LIMIT 512
#define PW_PATH_LIMIT 4096
static unsigned char pw_policy[PW_POLICY_LIMIT];
static const char *pw_roots[PW_ROOT_LIMIT];
static uint16_t pw_root_sizes[PW_ROOT_LIMIT];
static uint32_t pw_root_count;
static int pw_policy_ready;
static uint32_t pw_u32(const unsigned char *p) {
 return (uint32_t)p[0]|((uint32_t)p[1]<<8)|((uint32_t)p[2]<<16)|((uint32_t)p[3]<<24);
}
static int pw_canonical(const char *path,size_t size) {
 if(size<2||size>=PW_PATH_LIMIT||path[0]!='/'||path[size-1]=='/')return 0;
 size_t start=1;
 for(size_t i=1;i<=size;++i) {
  if(i<size&&(unsigned char)path[i]<32)return 0;
  if(i<size&&path[i]!='/')continue;
  size_t n=i-start;
  if(n==0||(n==1&&path[start]=='.')||(n==2&&path[start]=='.'&&path[start+1]=='.'))return 0;
  start=i+1;
 }
 return 1;
}
static void pw_load_policy(void) {
 struct statx facts;
 if(pw_stat(PW_POLICY_FD,&facts)!=0||facts.stx_uid!=0||facts.stx_mode&0222
    ||(facts.stx_mode&0170000)!=0100000||facts.stx_size<50||facts.stx_size>PW_POLICY_LIMIT)pw_refuse();
 long seals=pw_call(PW_FCNTL,PW_POLICY_FD,F_GET_SEALS,0,0,0,0);
 if(seals<0||(seals&15)!=15)pw_refuse();
 size_t size=(size_t)facts.stx_size;
 if(pw_call(PW_PREAD,PW_POLICY_FD,(long)pw_policy,size,0,0,0)!=(long)size)pw_refuse();
 if(!pw_equal((char *)pw_policy,"PWMOD01\0",8)||pw_u32(pw_policy+8)!=size)pw_refuse();
 pw_root_count=pw_u32(pw_policy+12);
 if(pw_root_count==0||pw_root_count>PW_ROOT_LIMIT)pw_refuse();
 size_t offset=48;
 for(uint32_t i=0;i<pw_root_count;++i) {
  if(size-offset<2)pw_refuse();
  uint16_t n=pw_policy[offset]|((uint16_t)pw_policy[offset+1]<<8);offset+=2;
  if(n>size-offset||!pw_canonical((char *)pw_policy+offset,n))pw_refuse();
  pw_roots[i]=(char *)pw_policy+offset;pw_root_sizes[i]=n;
  for(uint32_t prior=0;prior<i;++prior)
   if(pw_root_sizes[prior]==n&&pw_equal(pw_roots[prior],pw_roots[i],n))pw_refuse();
  offset+=n;
 }
 if(offset!=size)pw_refuse();
 pw_policy_ready=1;
}
static int pw_in_policy(const char *path,size_t size) {
 for(uint32_t i=0;i<pw_root_count;++i) {
  size_t n=pw_root_sizes[i];
  if(size>n&&path[n]=='/'&&pw_equal(path,pw_roots[i],n))return 1;
 }
 return 0;
}
#endif
