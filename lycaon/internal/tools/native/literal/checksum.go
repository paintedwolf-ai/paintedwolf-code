package literal

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"
)

// checksumAlgorithm selects the digest; shasum -a chooses among SHA sizes.
func (inv *invocation) checksumAlgorithm() (func() hash.Hash, error) {
	switch inv.name() {
	case "md5sum":
		return md5.New, nil
	case "sha1sum":
		return sha1.New, nil
	case "sha256sum":
		return sha256.New, nil
	case "sha512sum":
		return sha512.New, nil
	}
	value, ok := inv.value("-a")
	if !ok {
		return sha1.New, nil
	}
	switch value {
	case "1":
		return sha1.New, nil
	case "256":
		return sha256.New, nil
	case "512":
		return sha512.New, nil
	}
	return nil, inv.usage(value, "algorithm 1, 256 or 512")
}

// digest hashes exactly the captured representation, never a disk digest
// standing in for accepted editor text.
func digest(newHash func() hash.Hash, in *input) (string, error) {
	h := newHash()
	if _, err := io.Copy(h, in.reader()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runChecksum(inv *invocation) error {
	newHash, err := inv.checksumAlgorithm()
	if err != nil {
		return err
	}
	if inv.flag("-c") {
		return checkManifests(inv, newHash)
	}
	for _, operand := range inv.operandsOrStdin() {
		in, err := inv.openField(operand, false)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		sum, err := digest(newHash, in)
		if err != nil {
			return err
		}
		if err := inv.out.text(sum + "  " + operand.Value + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// checkManifests verifies "HEX  NAME" lines. Names are data bound through
// the ordinary path authority. A mismatch or unreadable file is status 1;
// an improperly formatted line is status 2.
func checkManifests(inv *invocation, newHash func() hash.Hash) error {
	size := newHash().Size() * 2
	failed, unreadable, malformed := 0, 0, 0
	for _, manifest := range inv.operandsOrStdin() {
		err := inv.manifestRecords(manifest, func(number int, line string) error {
			want, name, ok := manifestLine(line, size)
			if !ok {
				malformed++
				_, _ = fmt.Fprintf(inv.stderr, "%s: %s: %d: improperly formatted checksum line\n", inv.name(), manifest.Value, number)
				return nil
			}
			target, err := inv.open(name, false)
			if err != nil {
				return err
			}
			if target == nil {
				unreadable++
				if err := inv.out.text(name + ": FAILED open or read\n"); err != nil {
					return err
				}
				return nil
			}
			got, err := digest(newHash, target)
			if err != nil {
				return err
			}
			verdict := "OK"
			if got != want {
				verdict = "FAILED"
				failed++
			}
			return inv.out.text(name + ": " + verdict + "\n")
		})
		if err != nil {
			return err
		}
	}
	warn := func(count int, singular, plural string) {
		if count == 0 {
			return
		}
		form := singular
		if count != 1 {
			form = plural
		}
		_, _ = fmt.Fprintf(inv.stderr, "%s: WARNING: %d %s\n", inv.name(), count, form)
	}
	warn(unreadable, "listed file could not be read", "listed files could not be read")
	warn(failed, "computed checksum did NOT match", "computed checksums did NOT match")
	switch {
	case malformed > 0:
		inv.status = max(inv.status, 2)
	case failed > 0 || unreadable > 0:
		inv.status = max(inv.status, 1)
	}
	return nil
}

// manifestLine parses "HEX  NAME" or "HEX *NAME" for a digest of size hex digits.
func manifestLine(line string, size int) (string, string, bool) {
	if len(line) < size+3 || line[size] != ' ' || (line[size+1] != ' ' && line[size+1] != '*') {
		return "", "", false
	}
	want := strings.ToLower(line[:size])
	if _, err := hex.DecodeString(want); err != nil {
		return "", "", false
	}
	return want, line[size+2:], true
}
