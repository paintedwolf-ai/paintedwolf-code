package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateOpenPathDeniesSSH(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	p := DefaultOpenPolicy()
	if err := p.ValidateOpenPath(filepath.Join(home, ".ssh")); err == nil {
		t.Fatal("expected policy denial for .ssh")
	}
}

func TestValidateOpenPathAllowsTempUnderHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(home, "lycaon-policy-test")
	p := DefaultOpenPolicy()
	if err := p.ValidateOpenPath(dir); err != nil {
		t.Fatalf("unexpected denial: %v", err)
	}
}

func TestValidateOpenPathAllowsOutsideHome(t *testing.T) {
	p := DefaultOpenPolicy()
	dir := t.TempDir()
	if err := p.ValidateOpenPath(dir); err != nil {
		t.Fatalf("unexpected denial: %v", err)
	}
}

func TestTestOpenPolicyAllowsOutsideHome(t *testing.T) {
	p := TestOpenPolicy()
	dir := t.TempDir()
	if err := p.ValidateOpenPath(dir); err != nil {
		t.Fatalf("unexpected denial: %v", err)
	}
}

// Case variants identify the same denied root on case-insensitive filesystems.
func TestOpenPolicyDeniesCaseVariantSecretDirs(t *testing.T) {
	home := t.TempDir()
	p := OpenPolicy{DenyPathPrefixes: []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
	}}
	for _, deny := range []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".SSH"),
		filepath.Join(home, ".Ssh"),
		filepath.Join(home, ".SSH", "nested"),
		filepath.Join(home, ".AWS"),
	} {
		if err := p.ValidateOpenPath(deny); !errors.Is(err, ErrPolicyDenied) {
			t.Errorf("ValidateOpenPath(%q) err = %v, want ErrPolicyDenied", deny, err)
		}
	}
	// Neighbours that merely share a prefix stay open.
	for _, allow := range []string{
		filepath.Join(home, ".sshconfig"),
		filepath.Join(home, "ssh"),
		filepath.Join(home, "code"),
	} {
		if err := p.ValidateOpenPath(allow); err != nil {
			t.Errorf("ValidateOpenPath(%q) err = %v, want nil", allow, err)
		}
	}
}
