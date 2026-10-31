//go:build darwin || linux

package toolhost

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// fileUtilityGoldens are the catalog golden programs for du, cmp, od,
// strings, tree, iconv and envsubst; each runs through the command entry.
var fileUtilityGoldens = []string{"du-apparent-total", "cmp-silent-different", "cmp-list-offsets", "od-hex-bytes", "strings-min-length", "tree-gitignore-depth", "iconv-latin1-redirect", "envsubst-shell-format"}

func TestCommandFileUtilityGoldenProgramsRunThroughTheEntry(t *testing.T) {
	runEntryGoldens(t, fileUtilityGoldens)
}

// runEntryGoldens runs the named catalog golden programs through the command
// entry against their setup files, checking bytes, status and effects.
func runEntryGoldens(t *testing.T, ids []string) {
	t.Helper()
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var contract struct {
		Golden []struct {
			ID      string `json:"id"`
			Program string `json:"program"`
			Setup   struct {
				Files map[string]json.RawMessage `json:"files"`
			} `json:"setup"`
			Expected struct {
				Stdout          string `json:"stdout"`
				Stderr          string `json:"stderr"`
				Status          int    `json:"status"`
				SemanticEffects []struct {
					Kind, Path, Bytes string
				} `json:"semantic_effects"`
			} `json:"expected"`
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden contract", json.Unmarshal(catalog.Contracts["golden"], &contract))
	ran := 0
	for _, golden := range contract.Golden {
		if !slices.Contains(ids, golden.ID) {
			continue
		}
		ran++
		t.Run(golden.ID, func(t *testing.T) {
			env := newPipeSemanticsEnv(t)
			for name, raw := range golden.Setup.Files {
				var content string
				testutil.FailErr(t, "golden file "+name, json.Unmarshal(raw, &content))
				path := filepath.Join(env.Dir, filepath.FromSlash(name))
				testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Dir(path), 0o755))
				testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(content), 0o644))
			}
			out, err := env.Call(t, t.Context(), golden.Program)
			testutil.FailErr(t, golden.Program, err)
			result := decodePipeOutcome(t, out)
			if result.Refusal != nil || result.NativeGaps != 0 || result.Stdout != golden.Expected.Stdout || result.Stderr != golden.Expected.Stderr || result.Status != golden.Expected.Status {
				t.Fatalf("%s: %+v refusal %+v", golden.Program, result, result.Refusal)
			}
			for _, effect := range golden.Expected.SemanticEffects {
				data, err := os.ReadFile(filepath.Join(env.Dir, effect.Path))
				testutil.FailErr(t, "read "+effect.Path, err)
				if string(data) != effect.Bytes {
					t.Fatalf("%s holds %q, want %q", effect.Path, data, effect.Bytes)
				}
			}
		})
	}
	if ran != len(ids) {
		t.Fatalf("ran %d of %d goldens", ran, len(ids))
	}
}

// humanUnits is the -h rendering, written out here so the entry test does
// not borrow the implementation it checks.
func humanUnits(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10)
	}
	units, value, unit := "KMGTPE", float64(n)/1024, 0
	for value >= 1024 {
		value, unit = value/1024, unit+1
	}
	if value < 10 {
		tenths := int64(value * 10)
		if float64(tenths) < value*10 {
			tenths++
		}
		if tenths < 100 {
			return fmt.Sprintf("%d.%d%c", tenths/10, tenths%10, units[unit])
		}
	}
	whole := int64(value)
	if float64(whole) < value {
		whole++
	}
	return fmt.Sprintf("%d%c", whole, units[unit])
}

// TestCommandDuSummarizesAFolder: du -sh . is the allocated-block sum
// of the folder and --apparent-size is its files' byte sum.
func TestCommandDuSummarizesAFolder(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	root := filepath.Join(env.Dir, "proj")
	for name, content := range map[string]string{"a.txt": "alpha\n", "src/main.go": strings.Repeat("package main\n", 2000), "src/lib/x.go": "package lib\n", "empty/": ""} {
		path := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(name, "/")))
		if strings.HasSuffix(name, "/") {
			testutil.FailErr(t, "mkdir "+name, os.MkdirAll(path, 0o755))
			continue
		}
		testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(content), 0o644))
	}
	var blocks, bytes int64
	testutil.FailErr(t, "walk", filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		blocks += info.Sys().(*syscall.Stat_t).Blocks * 512
		if !info.IsDir() {
			bytes += info.Size()
		}
		return nil
	}))
	out, err := env.Call(t, t.Context(), `cd proj && du -sh . && du -s --apparent-size --bytes . && du -s --apparent-size .`)
	testutil.FailErr(t, "du", err)
	result := decodePipeOutcome(t, out)
	want := humanUnits(blocks) + "\t.\n" + strconv.FormatInt(bytes, 10) + "\t.\n" + strconv.FormatInt((bytes+1023)/1024, 10) + "\t.\n"
	if result.Refusal != nil || result.Status != 0 || result.Stdout != want {
		t.Fatalf("du outcome %+v, want stdout %q", result, want)
	}
}

// TestCommandDfReportsScratchInodes: df -i @scratch reports the scratch
// volume's inode totals.
func TestCommandDfReportsScratchInodes(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	out, err := env.Call(t, t.Context(), `df -i @scratch`)
	testutil.FailErr(t, "df", err)
	result := decodePipeOutcome(t, out)
	lines := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
	if result.Refusal != nil || result.Status != 0 || len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "Filesystem Inodes IUsed IFree IUse% Mounted on" {
		t.Fatalf("df -i @scratch %+v", result)
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 6 {
		t.Fatalf("df row %q", lines[1])
	}
	total, errT := strconv.ParseUint(fields[1], 10, 64)
	used, errU := strconv.ParseUint(fields[2], 10, 64)
	free, errF := strconv.ParseUint(fields[3], 10, 64)
	if errT != nil || errU != nil || errF != nil || total == 0 || used+free != total || !strings.HasSuffix(fields[4], "%") {
		t.Fatalf("df -i row %q is not an inode total", lines[1])
	}
	mount, err := os.Stat(strings.Join(fields[5:], " "))
	testutil.FailErr(t, "stat mount point", err)
	scratch, err := os.Stat(env.scratchDir)
	testutil.FailErr(t, "stat scratch", err)
	if mount.Sys().(*syscall.Stat_t).Dev != scratch.Sys().(*syscall.Stat_t).Dev {
		t.Fatalf("df mounted on %q is not the scratch volume", fields[5:])
	}
}

// TestCommandIconvRedirectPublishesWithCapturedRevisions: the input is pinned
// as a source revision and the redirect publishes at a destination pin.
func TestCommandIconvRedirectPublishesWithCapturedRevisions(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	testutil.FailErr(t, "write in.txt", os.WriteFile(filepath.Join(env.Dir, "in.txt"), []byte("na\xefve caf\xe9\n"), 0o644))
	out, err := env.Call(t, t.Context(), `iconv -f latin1 -t utf-8 in.txt > out.txt`)
	testutil.FailErr(t, "iconv", err)
	result := decodePipeOutcome(t, out)
	if result.Refusal != nil || result.Status != 0 || result.CompletedEffects != 1 {
		t.Fatalf("iconv outcome %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(env.Dir, "out.txt"))
	testutil.FailErr(t, "read out.txt", err)
	if string(data) != "naïve café\n" {
		t.Fatalf("out.txt holds %q", data)
	}
	for path, role := range map[string]string{"in.txt": "source", "out.txt": "destination"} {
		var pins int
		testutil.FailErr(t, "query "+path, env.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_revision_pins WHERE program_id = ? AND path = ? AND role = ?`, result.ProgramID, path, role).Scan(&pins))
		if pins != 1 {
			t.Fatalf("%s has %d %s pins, want 1", path, pins, role)
		}
	}
}

// TestCommandFileUtilitiesAreNativeAndRecordUncataloguedForms: each name
// binds to its native owner. A form outside the catalog is a recorded native
// gap: it refuses with the option code, or runs an installed program under
// that receipt fact.
func TestCommandFileUtilitiesAreNativeAndRecordUncataloguedForms(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	names := []string{"du", "df", "cmp", "od", "strings", "tree", "iconv", "envsubst"}
	out, err := env.Call(t, t.Context(), "type "+strings.Join(names, " "))
	testutil.FailErr(t, "type", err)
	var want strings.Builder
	for _, name := range names {
		want.WriteString(name + " is a native built-in\n")
	}
	if result := decodePipeOutcome(t, out); result.Stdout != want.String() || result.Status != 0 {
		t.Fatalf("type %+v", result)
	}
	for _, program := range []string{"du --inodes .", "df -T .", "cmp --zero a b", "od -w8 a", "strings -d a", "tree -C", "iconv -s a", "envsubst --help-me"} {
		out, err := env.Call(t, t.Context(), program)
		testutil.FailErr(t, program, err)
		result := decodePipeOutcome(t, out)
		if result.NativeGaps != 1 || result.Refusal != nil && result.Refusal.Code != "COMMAND_OPTION_UNSUPPORTED" {
			t.Fatalf("%s: %+v refusal %+v", program, result, result.Refusal)
		}
	}
}
