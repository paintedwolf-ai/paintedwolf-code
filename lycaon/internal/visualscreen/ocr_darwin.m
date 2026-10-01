#import <Foundation/Foundation.h>
#import <Vision/Vision.h>
#import "ocr_darwin.h"

COCRResult recognize_text_apple_vision(const unsigned char* data, size_t len) {
    COCRResult res = {0};
    if (data == NULL || len == 0) {
        return res;
    }
    @autoreleasepool {
        NSData* nsData = [NSData dataWithBytesNoCopy:(void*)data length:len freeWhenDone:NO];
        VNImageRequestHandler* handler = [[VNImageRequestHandler alloc] initWithData:nsData options:@{}];
        VNRecognizeTextRequest* request = [[VNRecognizeTextRequest alloc] init];
        request.recognitionLevel = VNRequestTextRecognitionLevelFast;
        request.usesLanguageCorrection = NO;

        NSError* error = nil;
        BOOL success = [handler performRequests:@[request] error:&error];
        if (!success || error != nil) {
            if (error != nil) {
                const char* errStr = [[error localizedDescription] UTF8String];
                res.error = strdup(errStr ? errStr : "vision request error");
            } else {
                res.error = strdup("vision request failed");
            }
            return res;
        }

        NSArray* observations = request.results;
        if (observations == nil || [observations count] == 0) {
            return res;
        }

        size_t count = [observations count];
        res.spans = (COCRSpan*)calloc(count, sizeof(COCRSpan));
        res.count = 0;

        for (VNRecognizedTextObservation* obs in observations) {
            NSArray<VNRecognizedText*>* candidates = [obs topCandidates:1];
            if (candidates == nil || [candidates count] == 0) {
                continue;
            }
            VNRecognizedText* top = [candidates firstObject];
            NSString* str = [top string];
            if (str == nil || [str length] == 0) {
                continue;
            }

            COCRSpan* span = &res.spans[res.count++];
            span->text = strdup([str UTF8String]);
            span->confidence = [top confidence];
            CGRect bbox = [obs boundingBox];
            span->x = (float)bbox.origin.x;
            span->y = (float)(1.0 - (bbox.origin.y + bbox.size.height));
            span->width = (float)bbox.size.width;
            span->height = (float)bbox.size.height;
        }
    }
    return res;
}

void free_ocr_result(COCRResult result) {
    if (result.error != NULL) {
        free(result.error);
    }
    if (result.spans != NULL) {
        for (size_t i = 0; i < result.count; i++) {
            if (result.spans[i].text != NULL) {
                free(result.spans[i].text);
            }
        }
        free(result.spans);
    }
}
