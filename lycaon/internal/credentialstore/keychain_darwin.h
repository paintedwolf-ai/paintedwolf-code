#include <Security/Security.h>

OSStatus pw_keychain_read(const char *service, const char *account, unsigned char **data, long *size);
OSStatus pw_keychain_create(const char *service, const char *account, const unsigned char *data, long size);
OSStatus pw_keychain_remove(const char *service, const char *account);
void pw_keychain_free(unsigned char *data, long size);
