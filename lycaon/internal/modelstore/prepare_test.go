package modelstore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPrepareBundleMeasuresOnlyExplicitPayloads(t *testing.T) {
	_, directory, signed := bundleFixture(t)
	supplement := testArtifact(t, []byte("tokenizer"))
	supplement.Name = "tokenizer.json"
	signed.Manifest.Files = []Artifact{supplement}
	testutil.FailErr(t, "create supplementary payload", os.WriteFile(filepath.Join(directory, supplement.Name), []byte("tokenizer"), 0600))
	recipe := signed.Manifest
	recipe.Artifact.Digest, recipe.Artifact.Bytes = "", 0
	recipe.Files = append([]Artifact(nil), recipe.Files...)
	for i := range recipe.Files {
		recipe.Files[i].Digest, recipe.Files[i].Bytes = "", 0
	}
	testutil.FailErr(t, "create unlisted input", os.WriteFile(filepath.Join(directory, "unlisted-private-key"), []byte("not a payload"), 0600))
	prepared, err := PrepareBundle(t.Context(), directory, recipe)
	testutil.FailErr(t, "prepare export", err)
	if !reflect.DeepEqual(prepared, signed.Manifest) {
		t.Fatalf("prepared = %+v, want %+v", prepared, signed.Manifest)
	}
	if recipe.Artifact.Digest != "" || recipe.Artifact.Bytes != 0 {
		t.Fatal("preparation mutated recipe")
	}
	for _, file := range recipe.Files {
		if file.Digest != "" || file.Bytes != 0 {
			t.Fatal("preparation mutated supplementary recipe")
		}
	}
}

func TestPrepareBundleRefusesInvalidInputs(t *testing.T) {
	for _, fault := range []string{"model", "revision", "device", "unnamed", "reserved", "traversal", "duplicate", "format", "measured", "too_many", "missing", "empty", "directory", "link", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			directory := t.TempDir()
			payload := filepath.Join(directory, "weights")
			testutil.FailErr(t, "create payload", os.WriteFile(payload, []byte("weights"), 0600))
			recipe := Manifest{Model: "model", Revision: "v1", Device: "mlx", Artifact: Artifact{Name: "weights", Format: "safetensors"}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch fault {
			case "model":
				recipe.Model = "../model"
			case "revision":
				recipe.Revision = ""
			case "device":
				recipe.Device = "../mlx"
			case "unnamed":
				recipe.Artifact.Name = ""
			case "reserved":
				recipe.Artifact.Name = "manifest.json"
			case "traversal":
				recipe.Artifact.Name = "../weights"
			case "duplicate":
				recipe.Files = []Artifact{recipe.Artifact}
			case "format":
				recipe.Artifact.Format = ""
			case "measured":
				recipe.Artifact.Bytes = 7
			case "too_many":
				recipe.Files = make([]Artifact, 65)
			case "missing":
				recipe.Artifact.Name = "absent"
			case "empty":
				testutil.FailErr(t, "empty payload", os.Truncate(payload, 0))
			case "directory":
				testutil.FailErr(t, "remove payload", os.Remove(payload))
				testutil.FailErr(t, "replace with directory", os.Mkdir(payload, 0700))
			case "link":
				testutil.FailErr(t, "rename payload", os.Rename(payload, payload+".source"))
				testutil.FailErr(t, "link payload", os.Symlink("weights.source", payload))
			case "canceled":
				cancel()
			}
			prepared, err := PrepareBundle(ctx, directory, recipe)
			if err == nil || prepared.Model != "" {
				t.Fatalf("invalid export accepted: %+v, %v", prepared, err)
			}
		})
	}
}
