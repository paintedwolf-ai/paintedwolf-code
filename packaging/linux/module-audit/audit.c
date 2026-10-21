/* Loaded in the glibc audit namespace without a DT_NEEDED dependency. */
#define _GNU_SOURCE
#include <link.h>
#include "paths.h"
unsigned int la_version(unsigned int version) {
 if(version<LAV_CURRENT)pw_refuse();
 pw_load_policy();
 unsigned char frame[40];
 for(size_t i=0;i<8;++i)frame[i]=(unsigned char)"PWAUD01\0"[i];
 for(size_t i=0;i<32;++i)frame[8+i]=pw_policy[16+i];
 if(pw_call(PW_WRITE,10,(long)frame,sizeof(frame),0,0,0)!=(long)sizeof(frame))pw_refuse();
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
 if(!pw_selected_name(name))pw_refuse();
 return 0;
}
