#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <string.h>
#include <errno.h>
#include <stdint.h>
#include <sys/ioccom.h>
#include <unistd.h>

// XNU counts open descriptors and active mappings against the unlinked vnode.
static int pw_guest_exclusive_reader(int fd) {
    uint32_t argument = 0;
    const unsigned long only_open_once = _IOWR('A', 21, uint32_t);
    return ffsctl(fd, only_open_once, &argument, 0) == 0 ? 0 : errno;
}

static OSStatus pw_verify_guest_code(SecStaticCodeRef code, CFStringRef team, CFStringRef identifier,
                                     SecCSFlags flags) {
    CFStringRef text = CFStringCreateWithFormat(NULL, NULL,
        CFSTR("anchor apple generic and certificate leaf[subject.OU] = \"%@\" and identifier \"%@\""),
        team, identifier);
    SecRequirementRef requirement = NULL;
    OSStatus status = text ? SecRequirementCreateWithString(text, kSecCSDefaultFlags, &requirement) : errSecAllocate;
    if (text) CFRelease(text);
    if (status == errSecSuccess) status = SecStaticCodeCheckValidity(code, flags, requirement);
    if (requirement) CFRelease(requirement);
    return status;
}

static OSStatus pw_guest_resource(SecStaticCodeRef outer, const char *resource, const UInt8 *bytes, CFIndex length) {
    CFStringRef path = CFStringCreateWithCString(NULL, resource, kCFStringEncodingUTF8);
    CFDataRef data = CFDataCreate(NULL, bytes, length);
    OSStatus status = path && data ? SecCodeValidateFileResource(outer, path, data, kSecCSDefaultFlags) : errSecAllocate;
    if (data) CFRelease(data);
    if (path) CFRelease(path);
    return status;
}

static OSStatus pw_verify_guest_runtime(SecStaticCodeRef outer, SecStaticCodeRef child) {
    SecCodeRef running = NULL;
    SecStaticCodeRef engine = NULL;
    CFDictionaryRef info = NULL;
    OSStatus status = SecCodeCopySelf(kSecCSDefaultFlags, &running);
    if (status != errSecSuccess) goto done;
    status = SecCodeCopyStaticCode(running, kSecCSDefaultFlags, &engine);
    if (status != errSecSuccess) goto done;
    status = SecCodeCopySigningInformation(engine, kSecCSSigningInformation, &info);
    if (status != errSecSuccess) goto done;
    CFStringRef team = CFDictionaryGetValue(info, kSecCodeInfoTeamIdentifier);
    if (!team || CFGetTypeID(team) != CFStringGetTypeID()) {
        status = errSecCSReqFailed;
        goto done;
    }
    status = pw_verify_guest_code(outer, team, CFSTR("dev.paintedwolf.code"),
                                  kSecCSStrictValidate | kSecCSDoNotValidateResources);
    if (status != errSecSuccess) goto done;
    status = pw_verify_guest_code(child, team, CFSTR("dev.paintedwolf.code.service-vm"),
                                  kSecCSStrictValidate);
done:
    if (info) CFRelease(info);
    if (engine) CFRelease(engine);
    if (running) CFRelease(running);
    return status;
}

static OSStatus pw_open_resource_envelope(const char *path, SecStaticCodeRef *code) {
    CFURLRef url = CFURLCreateFromFileSystemRepresentation(NULL, (const UInt8 *)path, strlen(path), false);
    if (!url) return errSecAllocate;
    OSStatus status = SecStaticCodeCreateWithPath(url, kSecCSDefaultFlags, code);
    CFRelease(url);
    return status;
}
