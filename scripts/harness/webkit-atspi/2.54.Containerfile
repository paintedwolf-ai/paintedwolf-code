# The distribution release that ships WebKitGTK 2.54.
FROM fedora:44
RUN dnf -y -q install \
    webkit2gtk4.1 gtk3 python3-gobject gobject-introspection at-spi2-core \
    xorg-x11-server-Xvfb xorg-x11-xauth util-linux dbus-daemon dbus-tools mesa-dri-drivers >/dev/null \
 && dnf clean all
