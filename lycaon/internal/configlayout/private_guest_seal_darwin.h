#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <string.h>

static OSStatus pw_verify_guest_code(const char *path, CFStringRef team, CFStringRef identifier,
                                     SecCSFlags flags, SecStaticCodeRef *result) {
    CFURLRef url = CFURLCreateFromFileSystemRepresentation(NULL, (const UInt8 *)path, strlen(path), false);
    if (!url) return errSecAllocate;
    SecStaticCodeRef code = NULL;
    OSStatus status = SecStaticCodeCreateWithPath(url, kSecCSDefaultFlags, &code);
    CFRelease(url);
    if (status != errSecSuccess) return status;
    CFStringRef text = CFStringCreateWithFormat(NULL, NULL,
        CFSTR("anchor apple generic and certificate leaf[subject.OU] = \"%@\" and identifier \"%@\""),
        team, identifier);
    SecRequirementRef requirement = NULL;
    status = text ? SecRequirementCreateWithString(text, kSecCSDefaultFlags, &requirement) : errSecAllocate;
    if (text) CFRelease(text);
    if (status == errSecSuccess) status = SecStaticCodeCheckValidity(code, flags, requirement);
    if (requirement) CFRelease(requirement);
    if (status == errSecSuccess) *result = code;
    else CFRelease(code);
    return status;
}

static OSStatus pw_verify_guest_runtime(const char *app, const char *helper, const UInt8 *manifest, CFIndex length) {
    SecCodeRef running = NULL;
    SecStaticCodeRef engine = NULL, outer = NULL, child = NULL;
    CFDictionaryRef info = NULL;
    CFDataRef data = NULL;
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
    status = pw_verify_guest_code(app, team, CFSTR("dev.paintedwolf.code"),
                                  kSecCSStrictValidate | kSecCSDoNotValidateResources, &outer);
    if (status != errSecSuccess) goto done;
    data = CFDataCreate(NULL, manifest, length);
    status = data ? SecCodeValidateFileResource(outer, CFSTR("Resources/service-guest/manifest.json"), data,
                                               kSecCSDefaultFlags) : errSecAllocate;
    if (status != errSecSuccess) goto done;
    status = pw_verify_guest_code(helper, team, CFSTR("dev.paintedwolf.code.service-vm"),
                                  kSecCSStrictValidate, &child);
done:
    if (child) CFRelease(child);
    if (outer) CFRelease(outer);
    if (data) CFRelease(data);
    if (info) CFRelease(info);
    if (engine) CFRelease(engine);
    if (running) CFRelease(running);
    return status;
}
