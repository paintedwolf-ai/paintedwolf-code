#ifndef OCR_DARWIN_H
#define OCR_DARWIN_H

#include <stddef.h>

typedef struct {
    char* text;
    float confidence;
    float x;
    float y;
    float width;
    float height;
} COCRSpan;

typedef struct {
    COCRSpan* spans;
    size_t count;
    char* error;
} COCRResult;

COCRResult recognize_text_apple_vision(const unsigned char* data, size_t len);
void free_ocr_result(COCRResult result);

#endif
