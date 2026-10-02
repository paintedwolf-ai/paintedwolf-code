#include "keychain_darwin.h"
#include <stdlib.h>
#include <string.h>
#import <LocalAuthentication/LocalAuthentication.h>

static CFMutableDictionaryRef identity_query(const char *service, const char *account) {
    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFStringRef service_value = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFStringRef account_value = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    if (!query || !service_value || !account_value) {
        if (query) CFRelease(query);
        if (service_value) CFRelease(service_value);
        if (account_value) CFRelease(account_value);
        return NULL;
    }
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, service_value);
    CFDictionarySetValue(query, kSecAttrAccount, account_value);
    CFDictionarySetValue(query, kSecUseDataProtectionKeychain, kCFBooleanTrue);
    CFDictionarySetValue(query, kSecAttrSynchronizable, kCFBooleanFalse);
    LAContext *context = [[LAContext alloc] init];
    if (!context) {
        CFRelease(query);
        CFRelease(service_value);
        CFRelease(account_value);
        return NULL;
    }
    context.interactionNotAllowed = YES;
    CFDictionarySetValue(query, kSecUseAuthenticationContext, (CFTypeRef)context);
    [context release];
    CFRelease(service_value);
    CFRelease(account_value);
    return query;
}

// Data-protection attributes may come back as CFNumber 0 rather than kCFBooleanFalse.
static Boolean pw_cf_false(CFTypeRef value) {
    if (!value) return true;
    if (CFGetTypeID(value) == CFBooleanGetTypeID()) return !CFBooleanGetValue((CFBooleanRef)value);
    if (CFGetTypeID(value) == CFNumberGetTypeID()) {
        long long number = 1;
        return CFNumberGetValue((CFNumberRef)value, kCFNumberLongLongType, &number) && number == 0;
    }
    return false;
}

OSStatus pw_keychain_read(const char *service, const char *account, unsigned char **data, long *size) {
    @autoreleasepool {
        *data = NULL;
        *size = 0;
        CFMutableDictionaryRef query = identity_query(service, account);
        if (!query) return errSecAllocate;
        CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
        CFDictionarySetValue(query, kSecReturnAttributes, kCFBooleanTrue);
        CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
        CFTypeRef result = NULL;
        OSStatus status = SecItemCopyMatching(query, &result);
        CFRelease(query);
        if (status != errSecSuccess) return status;
        if (!result || CFGetTypeID(result) != CFDictionaryGetTypeID()) {
            if (result) CFRelease(result);
            return errSecDecode;
        }
        CFDictionaryRef item = (CFDictionaryRef)result;
        CFTypeRef accessibility = CFDictionaryGetValue(item, kSecAttrAccessible);
        CFTypeRef synchronized = CFDictionaryGetValue(item, kSecAttrSynchronizable);
        CFDataRef value = (CFDataRef)CFDictionaryGetValue(item, kSecValueData);
        if (!value || CFGetTypeID(value) != CFDataGetTypeID() ||
            CFDataGetLength(value) < 1 || CFDataGetLength(value) > 65536) {
            CFRelease(result);
            return errSecDecode;
        }
        if (!accessibility || !CFEqual(accessibility, kSecAttrAccessibleWhenUnlockedThisDeviceOnly)) {
            CFRelease(result);
            return PW_KEYCHAIN_UNEXPECTED_ACCESSIBILITY;
        }
        if (!pw_cf_false(synchronized)) {
            CFRelease(result);
            return PW_KEYCHAIN_UNEXPECTED_SYNCHRONIZATION;
        }
        *size = CFDataGetLength(value);
        *data = malloc((size_t)*size);
        if (!*data) {
            CFRelease(result);
            return errSecAllocate;
        }
        memcpy(*data, CFDataGetBytePtr(value), (size_t)*size);
        CFRelease(result);
        return errSecSuccess;
    }
}

OSStatus pw_keychain_create(const char *service, const char *account, const unsigned char *data, long size) {
    @autoreleasepool {
        if (size < 1 || size > 65536) return errSecParam;
        CFMutableDictionaryRef query = identity_query(service, account);
        if (!query) return errSecAllocate;
        CFDataRef value = CFDataCreate(NULL, data, size);
        if (!value) { CFRelease(query); return errSecAllocate; }
        CFDictionarySetValue(query, kSecValueData, value);
        CFDictionarySetValue(query, kSecAttrAccessible, kSecAttrAccessibleWhenUnlockedThisDeviceOnly);
        CFDictionarySetValue(query, kSecAttrLabel, CFSTR("Painted Wolf Code credential vault"));
        OSStatus status = SecItemAdd(query, NULL);
        CFRelease(value);
        CFRelease(query);
        return status;
    }
}

OSStatus pw_keychain_remove(const char *service, const char *account) {
    @autoreleasepool {
        CFMutableDictionaryRef query = identity_query(service, account);
        if (!query) return errSecAllocate;
        OSStatus status = SecItemDelete(query);
        CFRelease(query);
        return status;
    }
}

void pw_keychain_free(unsigned char *data, long size) {
    if (data) { memset_s(data, (size_t)size, 0, (size_t)size); free(data); }
}
