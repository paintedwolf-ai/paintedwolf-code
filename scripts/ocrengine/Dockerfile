# syntax=docker/dockerfile:1.7
# Static Tesseract OCR engine for Painted Wolf Code on Linux.
#
# Every input is pinned by digest; scripts/ocrengine-build.sh supplies the
# arguments from lycaon/config/ocrengine/pin.yaml. The engine decodes nothing
# itself: the host hands it a PGM raster on stdin, so Leptonica is built without
# any image codec and the binary links only Tesseract, Leptonica, musl, and the
# GCC runtime libraries, all statically.
ARG BASE_IMAGE=alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
FROM ${BASE_IMAGE} AS build
ARG TARGETARCH
ARG ENGINE_VERSION
ARG TESSERACT_VERSION
ARG TESSERACT_SHA256
ARG LEPTONICA_VERSION
ARG LEPTONICA_SHA256
ARG TESSDATA_FAST_COMMIT
ARG ENG_SHA256
ARG OSD_SHA256
ARG MUSL_VERSION
ARG MUSL_COPYRIGHT_SHA256
ARG GCC_VERSION
ARG GPL3_SHA256
ARG GCC_RUNTIME_EXCEPTION_SHA256
ARG SOURCE_DATE_EPOCH=1704067200

RUN apk add --no-cache build-base autoconf automake libtool pkgconf curl tar gzip file

WORKDIR /src

RUN curl -fsSL -o leptonica.tar.gz \
      "https://github.com/DanBloomberg/leptonica/releases/download/${LEPTONICA_VERSION}/leptonica-${LEPTONICA_VERSION}.tar.gz" \
 && echo "${LEPTONICA_SHA256}  leptonica.tar.gz" | sha256sum -c - \
 && tar -xzf leptonica.tar.gz && rm leptonica.tar.gz
RUN cd "leptonica-${LEPTONICA_VERSION}" \
 && ./configure --prefix=/opt/lept --disable-shared --enable-static --disable-programs \
      --without-zlib --without-libpng --without-jpeg --without-giflib --without-libtiff \
      --without-libwebp --without-libwebpmux --without-libopenjpeg \
      CFLAGS="-O2 -ffile-prefix-map=/src=." \
 && make -j"$(nproc)" && make install

RUN curl -fsSL -o tesseract.tar.gz \
      "https://github.com/tesseract-ocr/tesseract/archive/refs/tags/${TESSERACT_VERSION}.tar.gz" \
 && echo "${TESSERACT_SHA256}  tesseract.tar.gz" | sha256sum -c - \
 && tar -xzf tesseract.tar.gz && rm tesseract.tar.gz
# The legacy recognizer stays in: orientation detection (osd) runs on it.
RUN cd "tesseract-${TESSERACT_VERSION}" \
 && ./autogen.sh \
 && PKG_CONFIG_PATH=/opt/lept/lib/pkgconfig ./configure --prefix=/opt/tess \
      --disable-shared --enable-static --disable-openmp --disable-graphics --disable-doc \
      --without-archive --without-curl \
      CXXFLAGS="-O2 -ffile-prefix-map=/src=." \
 && make -j"$(nproc)" LDFLAGS="-all-static" && make install

RUN mkdir -p /out/ocr/bin /out/ocr/tessdata/configs /out/ocr/licenses \
 && cp /opt/tess/bin/tesseract /out/ocr/bin/tesseract \
 && strip /out/ocr/bin/tesseract \
 && file /out/ocr/bin/tesseract | grep -q "statically linked" \
 && cp /opt/tess/share/tessdata/configs/tsv /out/ocr/tessdata/configs/tsv \
 && cd /out/ocr/tessdata \
 && curl -fsSL -o eng.traineddata "https://raw.githubusercontent.com/tesseract-ocr/tessdata_fast/${TESSDATA_FAST_COMMIT}/eng.traineddata" \
 && echo "${ENG_SHA256}  eng.traineddata" | sha256sum -c - \
 && curl -fsSL -o osd.traineddata "https://raw.githubusercontent.com/tesseract-ocr/tessdata_fast/${TESSDATA_FAST_COMMIT}/osd.traineddata" \
 && echo "${OSD_SHA256}  osd.traineddata" | sha256sum -c - \
 && cd /out/ocr/licenses \
 && cp "/src/tesseract-${TESSERACT_VERSION}/LICENSE" LICENSE-tesseract \
 && cp "/src/leptonica-${LEPTONICA_VERSION}/leptonica-license.txt" LICENSE-leptonica \
 && curl -fsSL -o COPYRIGHT-musl "https://git.musl-libc.org/cgit/musl/plain/COPYRIGHT?h=v${MUSL_VERSION}" \
 && echo "${MUSL_COPYRIGHT_SHA256}  COPYRIGHT-musl" | sha256sum -c - \
 && curl -fsSL -o GPL-3.0 "https://www.gnu.org/licenses/gpl-3.0.txt" \
 && echo "${GPL3_SHA256}  GPL-3.0" | sha256sum -c - \
 && curl -fsSL -o GCC-RUNTIME-LIBRARY-EXCEPTION-3.1 \
      "https://gcc.gnu.org/git/?p=gcc.git;a=blob_plain;f=COPYING.RUNTIME;hb=refs/tags/releases/gcc-${GCC_VERSION}" \
 && echo "${GCC_RUNTIME_EXCEPTION_SHA256}  GCC-RUNTIME-LIBRARY-EXCEPTION-3.1" | sha256sum -c -

# provenance.json records what went into the archive; the pin manifest in the
# application repository is what the host trusts.
RUN set -eu; cd /out/ocr; \
    BIN_SHA="$(sha256sum bin/tesseract | cut -d' ' -f1)"; \
    BIN_BYTES="$(stat -c %s bin/tesseract)"; \
    APK="$(apk info -v 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//')"; \
    GCC_PKG="$(apk info -v 2>/dev/null | grep '^gcc-' | head -1)"; \
    printf '%s\n' \
      '{' \
      "  \"engine_version\": \"${ENGINE_VERSION}\"," \
      "  \"platform\": \"linux/${TARGETARCH}\"," \
      "  \"tesseract\": {\"version\": \"${TESSERACT_VERSION}\", \"source_url\": \"https://github.com/tesseract-ocr/tesseract/archive/refs/tags/${TESSERACT_VERSION}.tar.gz\", \"sha256\": \"${TESSERACT_SHA256}\", \"license\": \"Apache-2.0\"}," \
      "  \"leptonica\": {\"version\": \"${LEPTONICA_VERSION}\", \"source_url\": \"https://github.com/DanBloomberg/leptonica/releases/download/${LEPTONICA_VERSION}/leptonica-${LEPTONICA_VERSION}.tar.gz\", \"sha256\": \"${LEPTONICA_SHA256}\", \"license\": \"BSD-2-Clause\"}," \
      "  \"tessdata_fast\": {\"commit\": \"${TESSDATA_FAST_COMMIT}\", \"license\": \"Apache-2.0\", \"files\": {\"eng.traineddata\": \"${ENG_SHA256}\", \"osd.traineddata\": \"${OSD_SHA256}\"}}," \
      "  \"musl\": {\"version\": \"${MUSL_VERSION}\", \"license\": \"MIT\"}," \
      "  \"gcc_runtime\": {\"package\": \"${GCC_PKG}\", \"license\": \"GPL-3.0-or-later WITH GCC-exception-3.1\"}," \
      "  \"build\": {\"base_image\": \"${BASE_IMAGE}\", \"packages\": \"${APK}\", \"source_date_epoch\": ${SOURCE_DATE_EPOCH}}," \
      "  \"binary\": {\"sha256\": \"${BIN_SHA}\", \"bytes\": ${BIN_BYTES}}" \
      '}' > provenance.json; \
    printf '%s\n' "${ENGINE_VERSION}" > VERSION

RUN set -eu; cd /out; \
    NAME="paintedwolf-tesseract-${ENGINE_VERSION}-linux-${TARGETARCH}"; \
    tar --sort=name --mtime="@${SOURCE_DATE_EPOCH}" --owner=0 --group=0 --numeric-owner -cf - ocr \
      | gzip -n -9 > "/out/${NAME}.tar.gz"; \
    sha256sum "${NAME}.tar.gz" > "${NAME}.tar.gz.sha256"; \
    cp ocr/provenance.json "${NAME}.provenance.json"

FROM scratch AS artifact
COPY --from=build /out/*.tar.gz /out/*.sha256 /out/*.provenance.json /
