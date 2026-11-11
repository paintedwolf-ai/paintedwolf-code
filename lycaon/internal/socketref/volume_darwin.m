#import <Foundation/Foundation.h>
#include <stdlib.h>
#include <string.h>
#include "volume_darwin.h"

int pw_socketref_volume_temporary(const char *path, char **result, char **error) {
    @autoreleasepool {
        NSString *name = path ? [NSString stringWithUTF8String:path] : nil;
        if (!name) return -1;
        NSError *failure = nil;
        NSURL *directory = [[NSFileManager defaultManager]
            URLForDirectory:NSItemReplacementDirectory
            inDomain:NSUserDomainMask
            appropriateForURL:[NSURL fileURLWithPath:name]
            create:YES error:&failure];
        if (!directory) {
            if (error && failure) *error = strdup([[failure localizedDescription] UTF8String]);
            return -1;
        }
        if (result) *result = strdup([[directory path] fileSystemRepresentation]);
        return 0;
    }
}
