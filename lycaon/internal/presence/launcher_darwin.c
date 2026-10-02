#include "launcher_darwin.h"
#include <stdio.h>
#include <unistd.h>
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

static int fail(char *reason, size_t reason_size, const char *message, OSStatus status) {
    if (status != errSecSuccess) {
        snprintf(reason, reason_size, "%s (OSStatus %d)", message, (int)status);
    } else {
        snprintf(reason, reason_size, "%s", message);
    }
    return 1;
}

// own_team_identifier copies the team that signed this process.
static CFStringRef own_team_identifier(OSStatus *status) {
    SecCodeRef self = NULL;
    *status = SecCodeCopySelf(kSecCSDefaultFlags, &self);
    if (*status != errSecSuccess) return NULL;
    SecStaticCodeRef static_self = NULL;
    *status = SecCodeCopyStaticCode(self, kSecCSDefaultFlags, &static_self);
    CFRelease(self);
    if (*status != errSecSuccess) return NULL;
    CFDictionaryRef info = NULL;
    *status = SecCodeCopySigningInformation(static_self, kSecCSSigningInformation, &info);
    CFRelease(static_self);
    if (*status != errSecSuccess) return NULL;
    CFStringRef team = CFDictionaryGetValue(info, kSecCodeInfoTeamIdentifier);
    if (team) CFRetain(team);
    CFRelease(info);
    return team;
}

// parent_code copies the running code of pid.
static SecCodeRef parent_code(pid_t pid, OSStatus *status) {
    CFNumberRef number = CFNumberCreate(NULL, kCFNumberIntType, &pid);
    if (!number) {
        *status = errSecAllocate;
        return NULL;
    }
    const void *keys[] = { kSecGuestAttributePid };
    const void *values[] = { number };
    CFDictionaryRef attributes = CFDictionaryCreate(NULL, keys, values, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFRelease(number);
    if (!attributes) {
        *status = errSecAllocate;
        return NULL;
    }
    SecCodeRef code = NULL;
    *status = SecCodeCopyGuestWithAttributes(NULL, attributes, kSecCSDefaultFlags, &code);
    CFRelease(attributes);
    return *status == errSecSuccess ? code : NULL;
}

int pw_presence_verify_parent(const char *identifier, char *reason, size_t reason_size) {
    pid_t parent = getppid();
    if (parent <= 1) {
        return fail(reason, reason_size, "the engine has no launching process", errSecSuccess);
    }
    OSStatus status = errSecSuccess;
    CFStringRef team = own_team_identifier(&status);
    if (!team) {
        return fail(reason, reason_size, "the engine carries no signing team", status);
    }
    CFStringRef text = CFStringCreateWithFormat(NULL, NULL,
        CFSTR("anchor apple generic and identifier \"%s\" and certificate leaf[subject.OU] = \"%@\""),
        identifier, team);
    CFRelease(team);
    if (!text) {
        return fail(reason, reason_size, "the launcher requirement could not be built", errSecAllocate);
    }
    SecRequirementRef requirement = NULL;
    status = SecRequirementCreateWithString(text, kSecCSDefaultFlags, &requirement);
    CFRelease(text);
    if (status != errSecSuccess) {
        return fail(reason, reason_size, "the launcher requirement could not be compiled", status);
    }
    SecCodeRef code = parent_code(parent, &status);
    if (!code) {
        CFRelease(requirement);
        return fail(reason, reason_size, "the launching process could not be inspected", status);
    }
    status = SecCodeCheckValidity(code, kSecCSDefaultFlags, requirement);
    CFRelease(code);
    CFRelease(requirement);
    if (status != errSecSuccess) {
        return fail(reason, reason_size, "the launching process is not the signed desktop app", status);
    }
    // A parent that exited during the check reparents this process.
    if (getppid() != parent) {
        return fail(reason, reason_size, "the launching process changed during verification", errSecSuccess);
    }
    return 0;
}
