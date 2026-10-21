/* Loaded in the glibc audit namespace without a DT_NEEDED dependency. */
#define _GNU_SOURCE
#include <link.h>
#include "paths.h"
static int handshake_pipe(void) {
 struct statx read,write;
 if(pw_stat(9,&read)||pw_stat(10,&write))return 0;
 long r=pw_call(PW_FCNTL,9,F_GETFL,0,0,0,0),w=pw_call(PW_FCNTL,10,F_GETFL,0,0,0,0);
 return pw_stat_complete(&read)&&pw_stat_complete(&write)&&read.stx_uid==0&&write.stx_uid==0&&(read.stx_mode&0170000)==0010000
  &&(write.stx_mode&0170000)==0010000&&pw_same_file(&read,&write)
  &&r>=0&&w>=0&&(r&O_ACCMODE)==O_RDONLY&&(w&O_ACCMODE)==O_WRONLY
  &&(r&O_NONBLOCK)&&(w&O_NONBLOCK);
}
unsigned int la_version(unsigned int version) {
 if(version<LAV_CURRENT)pw_refuse();
 pw_load_policy();
 unsigned char frame[40];
 for(size_t i=0;i<8;++i)frame[i]=(unsigned char)"PWAUD01\0"[i];
 for(size_t i=0;i<32;++i)frame[8+i]=pw_policy[16+i];
 if(handshake_pipe()&&pw_call(PW_WRITE,10,(long)frame,sizeof(frame),0,0,0)!=(long)sizeof(frame))pw_refuse();
 return LAV_CURRENT;
}
char *la_objsearch(const char *name,uintptr_t *cookie,unsigned int flag) {
 (void)cookie;(void)flag;
 if(!pw_policy_ready)pw_refuse();
 size_t n=pw_length(name,PW_PATH_LIMIT);
 if(n==0||n>=PW_PATH_LIMIT)pw_refuse();
 int slash=0;for(size_t i=0;i<n;++i)if(name[i]=='/')slash=1;
 if(!slash)return (char *)name; /* A SONAME must first resolve to a concrete candidate. */
 return pw_selected_name(name);
}
unsigned int la_objopen(struct link_map *map,Lmid_t namespace,uintptr_t *cookie) {
 (void)namespace;(void)cookie;
 if(!pw_policy_ready)pw_refuse();
 const char *name=map->l_name;
 if(!name||!*name)return 0; /* The executable is independently manifest-bound before exec. */
 if(pw_length(name,32)==sizeof("linux-vdso.so.1")-1&&pw_equal(name,"linux-vdso.so.1",sizeof("linux-vdso.so.1")-1))return 0;
 char *physical=pw_selected_name(name);
 if(!physical)pw_refuse();
 if(pw_length(physical,PW_PATH_LIMIT)==pw_guard_size&&pw_equal(physical,pw_guard_path,pw_guard_size)) {
  const Elf64_Ehdr *header=(const Elf64_Ehdr *)map->l_addr;
  if(header->e_ident[EI_MAG0]!=ELFMAG0||header->e_ident[EI_CLASS]!=ELFCLASS64
     ||header->e_ident[EI_DATA]!=ELFDATA2LSB||header->e_phnum>64||header->e_phentsize!=sizeof(Elf64_Phdr)
     ||header->e_phoff>65536)pw_refuse();
  const Elf64_Phdr *segments=(const Elf64_Phdr *)(map->l_addr+header->e_phoff);
  int writable=0,relro=0;
  for(unsigned i=0;i<header->e_phnum;++i) {
   uint64_t start=segments[i].p_vaddr,bytes=segments[i].p_memsz;
   if(pw_guard_offset<start||bytes<8||pw_guard_offset-start>bytes-8)continue;
   if(segments[i].p_type==PT_LOAD&&(segments[i].p_flags&PF_W))writable=1;
   if(segments[i].p_type==PT_GNU_RELRO)relro=1;
  }
  if(!writable||!relro||map->l_addr+pw_guard_offset<map->l_addr)pw_refuse();
  *(volatile uint64_t *)(map->l_addr+pw_guard_offset)=PW_GUARD_WITNESS;
 }
 return 0;
}
