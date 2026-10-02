#include <Security/Security.h>

// Read-back rejections the host names; Security never returns positive statuses.
#define PW_KEYCHAIN_UNEXPECTED_ACCESSIBILITY 1
#define PW_KEYCHAIN_UNEXPECTED_SYNCHRONIZATION 2

OSStatus pw_keychain_read(const char *service, const char *account, unsigned char **data, long *size);
OSStatus pw_keychain_create(const char *service, const char *account, const unsigned char *data, long size);
OSStatus pw_keychain_remove(const char *service, const char *account);
void pw_keychain_free(unsigned char *data, long size);
