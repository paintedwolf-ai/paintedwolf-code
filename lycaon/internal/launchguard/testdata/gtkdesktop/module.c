#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <fcntl.h>
#include <unistd.h>

__attribute__((constructor)) static void module_loaded(void) {
    const char *home = getenv("HOME");
    if (!home) return;
    char path[4096];
    int length = snprintf(path,sizeof(path),"%s/gtk-module-marker",home);
    if (length < 0 || (size_t)length >= sizeof(path)) return;
    int fd = open(path,O_WRONLY|O_CREAT|O_TRUNC|O_CLOEXEC,0600);
    if (fd >= 0) { ssize_t written = write(fd,"loaded\n",7); (void)written; (void)close(fd); }
}

void gtk_module_init(int *argc, char ***argv) { (void)argc; (void)argv; }
