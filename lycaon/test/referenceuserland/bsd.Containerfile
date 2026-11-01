# BSD reference userland: FreeBSD's utilities as ported by chimerautils, on the Debian
# snapshot the GNU userland uses, so every tool outside the BSD set is the same build in both.
ARG BASE
FROM ${BASE} AS build
ARG SNAPSHOT
ARG BUILD_PACKAGES
ARG LIBXO_URL
ARG LIBXO_SHA256
ARG CHIMERAUTILS_URL
ARG CHIMERAUTILS_SHA256
RUN set -eu; \
    printf 'Types: deb\nURIs: http://snapshot.debian.org/archive/debian/%s\nSuites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.pgp\n\nTypes: deb\nURIs: http://snapshot.debian.org/archive/debian-security/%s\nSuites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.pgp\n' \
      "${SNAPSHOT}" "${SNAPSHOT}" > /etc/apt/sources.list.d/debian.sources; \
    apt-get -q -o Acquire::Check-Valid-Until=false update; \
    DEBIAN_FRONTEND=noninteractive apt-get -q install -y --no-install-recommends ${BUILD_PACKAGES}; \
    mkdir -p /src/libxo /src/chimerautils; \
    curl -fsSL -o /src/libxo.tar.gz "${LIBXO_URL}"; \
    curl -fsSL -o /src/chimerautils.tar.gz "${CHIMERAUTILS_URL}"; \
    printf '%s  /src/libxo.tar.gz\n%s  /src/chimerautils.tar.gz\n' "${LIBXO_SHA256}" "${CHIMERAUTILS_SHA256}" | sha256sum -c -; \
    tar -xzf /src/libxo.tar.gz -C /src/libxo --strip-components 1; \
    tar -xzf /src/chimerautils.tar.gz -C /src/chimerautils --strip-components 1
# libxo links statically, so the userland image carries only chimerautils' own files.
RUN set -eu; \
    cd /src/libxo; \
    ./configure --prefix=/opt/libxo --enable-static --disable-shared --disable-gettext --disable-libxo-options; \
    make -j"$(nproc)"; \
    make install
RUN set -eu; \
    cd /src/chimerautils; \
    PKG_CONFIG_PATH=/opt/libxo/lib/pkgconfig meson setup /build --prefix=/usr --buildtype=release \
      -Dselinux=disabled -Dpam=disabled -Dtiny=disabled -Dinstall-lib=false; \
    ninja -C /build; \
    DESTDIR=/out ninja -C /build install; \
    rm -rf /out/usr/share; \
    ln -s bsdtar /out/usr/bin/tar

ARG BASE
FROM ${BASE}
ARG SNAPSHOT
ARG PACKAGES
ARG REPLACED
COPY --from=build /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list.d/debian.sources
COPY --from=build /out/usr/bin/ /opt/bootstrap/bin/
# The GNU packages chimerautils replaces leave entirely, so a command FreeBSD lacks is not
# found here instead of falling back to GNU. Their removal scripts run on a bootstrap copy of
# the BSD tools; dash stays to run them, and FreeBSD sh then replaces its /bin/sh.
RUN set -eu; \
    apt-get -q -o Acquire::Check-Valid-Until=false update; \
    DEBIAN_FRONTEND=noninteractive apt-get -q install -y --no-install-recommends ${PACKAGES}; \
    apt-get clean; \
    echo 'reference:x:1000:1000:reference:/home/reference:/bin/bash' >> /etc/passwd; \
    echo 'reference:x:1000:' >> /etc/group; \
    PATH="/opt/bootstrap/bin:${PATH}" dpkg --log=/dev/null --purge --force-remove-essential --force-depends ${REPLACED}; \
    PATH="/opt/bootstrap/bin:${PATH}" rm -rf /opt/bootstrap /var/lib/apt/lists/* /var/cache/apt/* /var/cache/debconf/*-old /var/cache/ldconfig/* /var/lib/dpkg/*-old /var/log/*
COPY --from=build /out/ /
