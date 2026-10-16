# The distribution release that ships WebKitGTK 2.50.
FROM ubuntu:22.04
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    gir1.2-webkit2-4.1 gir1.2-atspi-2.0 python3-gi at-spi2-core xvfb xauth dbus >/dev/null \
 && rm -rf /var/lib/apt/lists/*
