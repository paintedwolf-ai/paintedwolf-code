#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <sys/auxv.h>
#include <sys/stat.h>
#include <unistd.h>

static void json_string(const char *text) {
    putchar('"');
    for (const unsigned char *p = (const unsigned char *)text; *p; ++p) {
        if (*p == '"' || *p == '\\') {
            putchar('\\');
            putchar(*p);
        } else if (*p < 32) {
            printf("\\u%04x", *p);
        } else {
            putchar(*p);
        }
    }
    putchar('"');
}

int main(int argc, char **argv) {
    char *cwd = getcwd(NULL, 0);
    const char *execfn = (const char *)getauxval(AT_EXECFN);
    if (!cwd || !execfn) return 3;
    printf("{\"Args\":[");
    for (int i = 0; i < argc; ++i) {
        if (i) putchar(',');
        json_string(argv[i]);
    }
    printf("],\"Cwd\":");
    json_string(cwd);
    printf(",\"AuxExecutable\":");
    json_string(execfn);
    printf(",\"ProtocolFDs\":[");
    int comma = 0;
    for (int fd = 3; fd < 16; ++fd) {
        struct stat status;
        if (fstat(fd, &status) == 0) {
            printf("%s%d", comma ? "," : "", fd);
            comma = 1;
        }
    }
    printf("]}");
    free(cwd);
    return ferror(stdout) ? 4 : 0;
}
