package commandsurface_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestValidatedProgramIdentityRequiresLiveExecutionLease(t *testing.T) {
	memory := &programMemoryFixture{maximum: 4 << 20}
	parser, err := commandsurface.NewProgramParser(t.Context(), memory)
	testutil.FailErr(t, "admit parser", err)
	defer parser.Close()
	source := []byte("true; false")
	program, err := parser.Parse(source, commandsurface.ParseOptions{})
	testutil.FailErr(t, "validate execution unit", err)
	defer program.Close()
	before := memory.state
	digest, catalog, err := program.ValidatedProgramIdentity(t.Context())
	testutil.FailErr(t, "identify validated leased program", err)
	sum := sha256.Sum256(source)
	if digest != hex.EncodeToString(sum[:]) || catalog != toolcontract.LanguageCatalogDigest() || memory.state != before {
		t.Fatal("program identity differs or creates detached state")
	}
	testutil.FailErr(t, "close parser scratch", parser.Close())
	if _, _, err := program.ValidatedProgramIdentity(t.Context()); err != nil {
		t.Fatal("scratch release erased retained execution proof")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := program.ValidatedProgramIdentity(ctx); err == nil {
		t.Fatal("canceled admission obtained identity")
	}
	testutil.FailErr(t, "release execution program", program.Close())
	if _, _, err := program.ValidatedProgramIdentity(t.Context()); err == nil {
		t.Fatal("closed program retained execution proof")
	}
}

func TestInspectionAndUnvalidatedProgramsCannotProveExecutionAdmission(t *testing.T) {
	program, err := commandsurface.ParseProgram([]byte("true"), commandsurface.ParseOptions{})
	testutil.FailErr(t, "inspect valid program", err)
	if _, _, err := program.ValidatedProgramIdentity(t.Context()); err == nil {
		t.Fatal("inspection-only copy qualified execution admission")
	}
	var absent *commandsurface.ParsedProgram
	if _, _, err := absent.ValidatedProgramIdentity(t.Context()); err == nil {
		t.Fatal("nil program qualified admission")
	}
	var zero commandsurface.ParsedProgram
	if _, _, err := zero.ValidatedProgramIdentity(t.Context()); err == nil {
		t.Fatal("unvalidated zero-value program qualified admission")
	}
}
