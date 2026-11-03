// Package referenceuserland runs shell programs in pinned GNU, BSD and busybox
// userlands, the reference implementations the command language is compared with.
//
// Each userland is a Linux container image built from userlands.json: a base image
// pinned by digest, packages pinned to exact versions from one Debian archive
// snapshot, and source archives pinned by hash. An image is tagged only after its
// file tree hashes to the digest recorded for its architecture, so a tagged image
// is the reproduced one.
//
// Every program runs in the same fixed environment, whichever userland runs it:
//
//   - a fresh workspace at /workspace, holding exactly the seeded entries, each
//     owned by the reference user and last modified at [SeedTime];
//   - user and group "reference" (1000:1000), umask 022, host name "reference";
//   - an empty HOME at /home/reference and an empty /tmp;
//   - the environment from [Environment], and nothing inherited;
//   - standard input from /dev/null and no network beyond loopback;
//   - the userland's shell (bash, or busybox sh) running the program with -c.
//
// When the shell exits, processes it left behind are killed, so no program can
// affect the next one in a batch.
package referenceuserland
