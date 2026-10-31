# GNU reference userland: Debian's GNU tools, installed from one archive snapshot.
ARG BASE
FROM ${BASE}
ARG SNAPSHOT
ARG PACKAGES
RUN set -eu; \
    printf 'Types: deb\nURIs: http://snapshot.debian.org/archive/debian/%s\nSuites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.pgp\n\nTypes: deb\nURIs: http://snapshot.debian.org/archive/debian-security/%s\nSuites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.pgp\n' \
      "${SNAPSHOT}" "${SNAPSHOT}" > /etc/apt/sources.list.d/debian.sources; \
    apt-get -q -o Acquire::Check-Valid-Until=false update; \
    DEBIAN_FRONTEND=noninteractive apt-get -q install -y --no-install-recommends ${PACKAGES}; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/* /var/cache/apt/* /var/cache/debconf/*-old /var/cache/ldconfig/* /var/lib/dpkg/*-old /var/log/*; \
    echo 'reference:x:1000:1000:reference:/home/reference:/bin/bash' >> /etc/passwd; \
    echo 'reference:x:1000:' >> /etc/group
