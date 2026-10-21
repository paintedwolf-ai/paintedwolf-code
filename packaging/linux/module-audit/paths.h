#ifndef PW_MODULE_PATHS_H
#define PW_MODULE_PATHS_H
#include "policy.h"
#define PW_NAME_LIMIT 1024
static char pw_names[PW_NAME_LIMIT][PW_PATH_LIMIT];
static unsigned pw_next_name;
static int pw_root_controlled(const struct statx *facts,int directory) {
 return facts->stx_uid==0&&!(facts->stx_mode&0022)
  &&(facts->stx_mode&0170000)==(directory?0040000:0100000);
}
static int pw_same_file(const struct statx *a,const struct statx *b) {
 return a->stx_ino==b->stx_ino&&a->stx_dev_major==b->stx_dev_major&&a->stx_dev_minor==b->stx_dev_minor;
}
static int pw_physical_chain(const char *path,const struct statx *selected) {
 int directory=(int)pw_call(PW_OPENAT,AT_FDCWD,(long)"/",O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC,0,0,0);
 if(directory<0)return 0;
 struct statx facts;
 if(pw_stat(directory,&facts)!=0||!pw_root_controlled(&facts,1)){pw_close(directory);return 0;}
 size_t offset=1;
 while(path[offset]) {
  char component[PW_PATH_LIMIT];size_t n=0;
  while(path[offset]&&path[offset]!='/')component[n++]=path[offset++];
  component[n]=0;int last=path[offset]==0;
  int fd=(int)pw_call(PW_OPENAT,directory,(long)component,O_PATH|O_NOFOLLOW|O_CLOEXEC|(last?0:O_DIRECTORY),0,0,0);
  pw_close(directory);
  if(fd<0)return 0;
  if(pw_stat(fd,&facts)!=0||!pw_root_controlled(&facts,!last)){pw_close(fd);return 0;}
  if(last){int same=pw_same_file(&facts,selected);pw_close(fd);return same;}
  directory=fd;++offset;
 }
 pw_close(directory);return 0;
}
static char *pw_selected_name(const char *name) {
 size_t n=pw_length(name,PW_PATH_LIMIT);
 if(n==0||n>=PW_PATH_LIMIT)pw_refuse();
 int fd=(int)pw_call(PW_OPENAT,AT_FDCWD,(long)name,O_PATH|O_CLOEXEC,0,0,0);
 if(fd==-2||fd==-20)return NULL; /* Candidate absent; the loader may search the next root. */
 if(fd<0)pw_refuse();
 struct statx facts;
 if(pw_stat(fd,&facts)!=0||!pw_root_controlled(&facts,0)){pw_close(fd);pw_refuse();}
 char link[64]="/proc/self/fd/";size_t length=14;unsigned value=(unsigned)fd;
 char digits[16];size_t count=0;
 do{digits[count++]=(char)('0'+value%10);value/=10;}while(value);
 while(count)link[length++]=digits[--count];link[length]=0;
 char physical[PW_PATH_LIMIT];
 long size=pw_call(PW_READLINKAT,AT_FDCWD,(long)link,(long)physical,PW_PATH_LIMIT-1,0,0);
 if(size<=0||size>=PW_PATH_LIMIT-1){pw_close(fd);pw_refuse();}
 physical[size]=0;
 if(!pw_canonical(physical,(size_t)size)||!pw_in_policy(physical,(size_t)size)||!pw_physical_chain(physical,&facts)){
  pw_close(fd);pw_refuse();
 }
 pw_close(fd);
 /* The rtld lock serializes object searches; names remain valid for every namespace. */
 for(unsigned i=0;i<pw_next_name;++i)
  if(pw_equal(pw_names[i],physical,(size_t)size+1))return pw_names[i];
 if(pw_next_name==PW_NAME_LIMIT)pw_refuse();
 char *result=pw_names[pw_next_name++];
 for(size_t i=0;i<=(size_t)size;++i)result[i]=physical[i];
 return result;
}
#endif
