#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <spawn.h>
#include <stdint.h>
#include <sys/wait.h>
#include <unistd.h>

static OSStatus verify_process(pid_t pid, const char *cdhash) {
    CFStringRef text = CFStringCreateWithFormat(NULL, NULL, CFSTR("cdhash H\"%s\""), cdhash);
    SecRequirementRef requirement = NULL;
    OSStatus status = text ? SecRequirementCreateWithString(text, kSecCSDefaultFlags, &requirement) : errSecAllocate;
    if (text) CFRelease(text);
    if (status != errSecSuccess) return status;
    CFNumberRef number = CFNumberCreate(NULL, kCFNumberIntType, &pid);
    const void *keys[] = {kSecGuestAttributePid}, *values[] = {number};
    CFDictionaryRef attributes = number ? CFDictionaryCreate(NULL, keys, values, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks) : NULL;
    SecCodeRef code = NULL;
    status = attributes ? SecCodeCopyGuestWithAttributes(NULL, attributes, kSecCSDefaultFlags, &code) : errSecAllocate;
    if (status == errSecSuccess) status = SecCodeCheckValidity(code, kSecCSDefaultFlags, requirement);
    if (code) CFRelease(code);
    if (attributes) CFRelease(attributes);
    if (number) CFRelease(number);
    CFRelease(requirement);
    return status;
}

int pw_guest_spawn(const char *path, const char *cdhash, const int *fds, pid_t *pid) {
    posix_spawnattr_t attrs;
    posix_spawn_file_actions_t files;
    int error = posix_spawnattr_init(&attrs);
    if (error) return error;
    error = posix_spawn_file_actions_init(&files);
    if (error) {
        posix_spawnattr_destroy(&attrs);
        return error;
    }
    error = posix_spawnattr_setflags(&attrs, POSIX_SPAWN_CLOEXEC_DEFAULT | POSIX_SPAWN_START_SUSPENDED);
    if (!error) error = posix_spawn_file_actions_addchdir_np(&files, "/");
    if (!error) error = posix_spawn_file_actions_addopen(&files, 0, "/dev/null", O_RDONLY, 0);
    if (!error) error = posix_spawn_file_actions_addopen(&files, 1, "/dev/null", O_WRONLY, 0);
    if (!error) error = posix_spawn_file_actions_addinherit_np(&files, STDERR_FILENO);
    for (int i = 0; i < 4 && !error; i++) error = posix_spawn_file_actions_adddup2(&files, fds[i], 3+i);
    char *argv[] = {(char *)path, NULL}, *env[] = {NULL};
    if (!error) error = posix_spawn(pid, path, &files, &attrs, argv, env);
    posix_spawn_file_actions_destroy(&files);
    posix_spawnattr_destroy(&attrs);
    if (error) return error;
    error = verify_process(*pid, cdhash);
    if (!error && kill(*pid, SIGCONT) != 0) error = errno;
    if (error) {
        kill(*pid, SIGKILL);
        while (waitpid(*pid, NULL, 0) < 0 && errno == EINTR) {}
    }
    return error;
}
