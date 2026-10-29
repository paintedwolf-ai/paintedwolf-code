# busybox reference userland: the upstream busybox image as published.
ARG BASE
FROM ${BASE}
RUN echo 'reference:x:1000:1000:reference:/home/reference:/bin/sh' >> /etc/passwd \
 && echo 'reference:x:1000:' >> /etc/group
