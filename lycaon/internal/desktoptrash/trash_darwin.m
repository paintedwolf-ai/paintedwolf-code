#import <Foundation/Foundation.h>
#import "trash_darwin.h"

int pw_trash_item(const char *c_path, char **c_result, char **c_err) {
    if (c_path == NULL) {
        if (c_err) *c_err = strdup("empty path");
        return -1;
    }
    @autoreleasepool {
        NSString *pathStr = [NSString stringWithUTF8String:c_path];
        if (!pathStr) {
            if (c_err) *c_err = strdup("invalid UTF-8 path");
            return -1;
        }
        NSURL *url = [NSURL fileURLWithPath:pathStr];
        NSError *error = nil;
        NSURL *result = nil;
        BOOL success = [[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:&result error:&error];
        if (!success) {
            if (c_err && error) {
                *c_err = strdup([[error localizedDescription] UTF8String]);
            }
            return -1;
        }
        if (c_result && result) *c_result = strdup([[result path] UTF8String]);
        return 0;
    }
}
