package bundleverify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/dotversion"
	"github.com/lycaon/lycaon/internal/platformfloor"
)

// Bundle layout required by the release audit.
const (
	sidecarRelPath      = "Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw"
	logsCLIRelPath      = "Contents/MacOS/pw-logs"
	documentCoreRelPath = "Contents/MacOS/pw-document-core"
	decideEngineRelPath = "Contents/MacOS/bialy"
	// decideMetallibRelPath is MLX's compiled kernels, which the engine needs on Apple silicon.
	decideMetallibRelPath = "Contents/Resources/engine-root/decide/mlx.metallib"
	engineSchemasRelPath  = "Contents/Resources/engine-root/schemas"
	engineBrowserRelPath  = "Contents/Resources/engine-root/browser"
	noticesRelPath        = "Contents/Resources/THIRD-PARTY-NOTICES.md"
	// noticesMinBytes rejects empty or stub notices.
	noticesMinBytes = 4_096

	engineGitRelPath = "Contents/Resources/engine-root/gitengine"
	// gitengineMaxBytes rejects materialized multi-call binaries.
	gitengineMaxBytes = 40 * 1024 * 1024
)

// systemDylibPrefixes are the permitted runtime load roots.
var systemDylibPrefixes = []string{
	"/usr/lib/",
	"/System/",
	"@rpath/",
	"@executable_path/",
	"@loader_path/",
}

// buildPathPrefix starts an absolute local source path.
const buildPathPrefix = "/Users/"

// goSourceSuffix distinguishes source paths from embedded content.
const goSourceSuffix = ".go"

// Options configures one audit.
type Options struct {
	AppPath       string
	DMGPath       string // optional
	RequireSigned bool
	Runner        Runner // nil ⇒ execRunner{}
}

// Verify runs the configured bundle audit.
func Verify(ctx context.Context, opts Options) (Report, error) {
	if opts.AppPath == "" {
		return Report{}, errors.New("bundleverify: AppPath is required")
	}
	runner := opts.Runner
	if runner == nil {
		runner = execRunner{}
	}

	report := Report{
		App:           opts.AppPath,
		DMG:           opts.DMGPath,
		RequireSigned: opts.RequireSigned,
	}

	var findings []Finding
	findings = append(findings, checkLayout(opts.AppPath)...)
	findings = append(findings, checkGitengineWeight(opts.AppPath)...)

	machOs, err := WalkMachO(opts.AppPath)
	if err != nil {
		return Report{}, err
	}
	report.MachOCount = len(machOs)

	findings = append(findings, checkMachOs(opts.AppPath, machOs)...)
	findings = append(findings, checkBuildPathLeak(opts.AppPath)...)
	signingFindings := checkSigning(ctx, runner, opts, machOs)
	findings = append(findings, signingFindings...)
	if opts.RequireSigned && slices.ContainsFunc(signingFindings, func(f Finding) bool { return f.Severity == SeverityError }) {
		findings = append(findings, Finding{
			Code: CodeOpenGrepVerificationUnavailable, Severity: SeverityError, Path: engineRootRelPath,
			Detail: map[string]string{"reason": "packaged sidecar was not executed because required signature checks failed"},
		})
	} else {
		findings = append(findings, checkOpenGrep(ctx, runner, opts)...)
		findings = append(findings, checkDocumentCore(ctx, runner, opts)...)
		findings = append(findings, checkCredentialProtection(ctx, runner, opts)...)
	}

	sortFindings(findings)
	report.Findings = findings
	return report, nil
}

// bundleRel renders a path relative to the .app for reporting.
func bundleRel(appPath, path string) string {
	rel, err := filepath.Rel(appPath, path)
	if err != nil {
		return path
	}
	return rel
}

func checkLayout(appPath string) []Finding {
	var findings []Finding

	for _, executable := range []struct{ rel, code string }{
		{sidecarRelPath, CodeSidecarMissing},
		{logsCLIRelPath, CodeLogsCLIMissing},
		{documentCoreRelPath, CodeDocumentCoreMissing},
		{decideEngineRelPath, CodeDecideEngineMissing},
	} {
		info, err := os.Stat(filepath.Join(appPath, executable.rel))
		switch {
		case err != nil:
			findings = append(findings, Finding{
				Code: executable.code, Severity: SeverityError, Path: executable.rel,
				Detail: map[string]string{"reason": "not found"},
			})
		case info.Mode().Perm()&0o111 == 0:
			findings = append(findings, Finding{
				Code: executable.code, Severity: SeverityError, Path: executable.rel,
				Detail: map[string]string{"reason": "not executable"},
			})
		}
	}
	if st, err := os.Stat(filepath.Join(appPath, decideMetallibRelPath)); err != nil || st.IsDir() || st.Size() == 0 {
		findings = append(findings, Finding{
			Code: CodeDecideEngineMissing, Severity: SeverityError, Path: decideMetallibRelPath,
			Detail: map[string]string{"reason": "MLX Metal library not found"},
		})
	}
	findings = append(findings, checkDecideModel(appPath)...)

	for _, rel := range []string{engineSchemasRelPath, engineBrowserRelPath} {
		if _, err := os.Stat(filepath.Join(appPath, rel)); err != nil {
			findings = append(findings, Finding{
				Code: CodeEngineResourceMissing, Severity: SeverityError, Path: rel,
			})
		}
	}

	noticesPath := filepath.Join(appPath, noticesRelPath)
	info, err := os.Stat(noticesPath)
	switch {
	case err != nil:
		findings = append(findings, Finding{
			Code: CodeNoticesMissing, Severity: SeverityError, Path: noticesRelPath,
			Detail: map[string]string{"reason": "not found"},
		})
	case info.IsDir():
		findings = append(findings, Finding{
			Code: CodeNoticesMissing, Severity: SeverityError, Path: noticesRelPath,
			Detail: map[string]string{"reason": "is a directory"},
		})
	case info.Size() < noticesMinBytes:
		findings = append(findings, Finding{
			Code: CodeNoticesMissing, Severity: SeverityError, Path: noticesRelPath,
			Detail: map[string]string{
				"reason": "too small",
				"got":    strconv.FormatInt(info.Size(), 10),
				"want":   strconv.Itoa(noticesMinBytes),
			},
		})
	}

	return findings
}

func checkGitengineWeight(appPath string) []Finding {
	root := filepath.Join(appPath, engineGitRelPath)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		// Preflight reports an absent Git engine.
		return nil
	}

	var findings []Finding
	status := filepath.Join(root, "libexec", "git-core", "git-status")
	if st, err := os.Lstat(status); err == nil && st.Mode().IsRegular() && st.Size() > 1024*1024 {
		findings = append(findings, Finding{
			Code:     CodeGitengineBloated,
			Severity: SeverityError,
			Path:     filepath.Join(engineGitRelPath, "libexec/git-core/git-status"),
			Detail: map[string]string{
				"reason": "materialized multi-call alias",
				"got":    strconv.FormatInt(st.Size(), 10),
			},
		})
	}
	if _, err := os.Lstat(filepath.Join(root, "libexec", "git-core", "libclrgc.dylib")); err == nil {
		findings = append(findings, Finding{
			Code:     CodeGitengineBloated,
			Severity: SeverityError,
			Path:     filepath.Join(engineGitRelPath, "libexec/git-core/libclrgc.dylib"),
			Detail:   map[string]string{"reason": "GCM residue"},
		})
	}

	var total int64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Mode().IsRegular() {
			total += st.Size()
		}
		return nil
	})
	if total > gitengineMaxBytes {
		findings = append(findings, Finding{
			Code:     CodeGitengineBloated,
			Severity: SeverityError,
			Path:     engineGitRelPath,
			Detail: map[string]string{
				"reason": "over size budget",
				"got":    strconv.FormatInt(total, 10),
				"want":   strconv.FormatInt(gitengineMaxBytes, 10),
			},
		})
	}
	return findings
}

func checkMachOs(appPath string, machOs []MachOFacts) []Finding {
	var findings []Finding

	shipped := platformfloor.DarwinArches()
	floor := platformfloor.MacOSMin()

	for _, m := range machOs {
		rel := bundleRel(appPath, m.Path)

		var haveShipped bool
		for _, s := range m.Slices {
			if s.GoArch != "" && slices.Contains(shipped, s.GoArch) {
				haveShipped = true
				continue
			}
			if m.Fat {
				findings = append(findings, Finding{
					Code: CodeArchExtraSlice, Severity: SeverityWarn, Path: rel,
					Detail: map[string]string{"arch": archLabel(s.GoArch)},
				})
				continue
			}
			findings = append(findings, Finding{
				Code: CodeArchUnexpected, Severity: SeverityError, Path: rel,
				Detail: map[string]string{"arch": archLabel(s.GoArch), "want": strings.Join(shipped, ",")},
			})
		}
		if !haveShipped {
			findings = append(findings, Finding{
				Code: CodeArchUnexpected, Severity: SeverityError, Path: rel,
				Detail: map[string]string{"want": strings.Join(shipped, ","), "reason": "no shipped slice"},
			})
		}

		findings = append(findings, checkSliceFloorAndDylibs(rel, floor, m.Slices)...)
	}

	return findings
}

func checkSliceFloorAndDylibs(rel, floor string, slicesIn []SliceFacts) []Finding {
	var findings []Finding

	for _, s := range slicesIn {
		// Each slice needs a distinct finding label.
		arch := archLabel(s.GoArch)

		if s.MinOS == "" {
			findings = append(findings, Finding{
				Code: CodeMinOSMissing, Severity: SeverityWarn, Path: rel,
				Detail: map[string]string{"arch": arch},
			})
		} else if cmp, err := dotversion.Compare(s.MinOS, floor); err == nil && cmp > 0 {
			findings = append(findings, Finding{
				Code: CodeMinOSAboveFloor, Severity: SeverityError, Path: rel,
				Detail: map[string]string{"arch": arch, "want": floor, "got": s.MinOS},
			})
		}

		for _, dylib := range s.Dylibs {
			if isSystemDylib(dylib) {
				continue
			}
			findings = append(findings, Finding{
				Code: CodeNonSystemDylib, Severity: SeverityError, Path: rel,
				Detail: map[string]string{"arch": arch, "dylib": dylib},
			})
		}
	}

	return findings
}

func archLabel(goArch string) string {
	if goArch == "" {
		return "unknown"
	}
	return goArch
}

func isSystemDylib(path string) bool {
	for _, prefix := range systemDylibPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func checkBuildPathLeak(appPath string) []Finding {
	var findings []Finding
	for _, rel := range []string{sidecarRelPath, logsCLIRelPath, decideEngineRelPath} {
		data, err := os.ReadFile(filepath.Join(appPath, rel)) // #nosec G304 -- bundle-relative path from closed layout constants
		if err != nil {
			// Layout validation reports missing binaries.
			continue
		}
		leak, ok := findBuildPathLeak(string(data))
		if !ok {
			continue
		}
		findings = append(findings, Finding{
			Code: CodeBuildPathLeak, Severity: SeverityError, Path: rel,
			Detail: map[string]string{"path": leak},
		})
	}
	return findings
}

// findBuildPathLeak returns the first embedded local source path.
func findBuildPathLeak(data string) (string, bool) {
	for offset := 0; ; {
		idx := strings.Index(data[offset:], buildPathPrefix)
		if idx < 0 {
			return "", false
		}
		start := offset + idx
		candidate := data[start : start+pathRunLen(data[start:])]
		if strings.HasSuffix(candidate, goSourceSuffix) {
			return candidate, true
		}
		offset = start + len(buildPathPrefix)
	}
}

// pathRunLen is the length of the filesystem-path-shaped run at the head of s.
func pathRunLen(s string) int {
	for i := 0; i < len(s); i++ {
		if !isPathByte(s[i]) {
			return i
		}
	}
	return len(s)
}

func isPathByte(b byte) bool {
	if b <= ' ' || b >= 0x7f {
		return false
	}
	switch b {
	case '"', '\'', '`', '<', '>', '|', '*', '?', ';', ':', ',', ')', '(', '[', ']', '{', '}':
		return false
	}
	return true
}

// checkDecideModel requires every pinned checkpoint file at its pinned size, plus the
// completion marker the host checks.
func checkDecideModel(appPath string) []Finding {
	model := bialy.ShippedModel
	dir := engineRootRelPath + "/" + model.BundledRel()
	var findings []Finding
	for _, f := range model.Files {
		rel := dir + "/" + f.Path
		st, err := os.Stat(filepath.Join(appPath, filepath.FromSlash(rel)))
		switch {
		case err != nil || st.IsDir():
			findings = append(findings, Finding{
				Code: CodeDecideModelMissing, Severity: SeverityError, Path: rel,
				Detail: map[string]string{"reason": "not found"},
			})
		case st.Size() != f.Size:
			findings = append(findings, Finding{
				Code: CodeDecideModelMissing, Severity: SeverityError, Path: rel,
				Detail: map[string]string{"reason": "size differs from the pin", "size": strconv.FormatInt(st.Size(), 10), "pinned": strconv.FormatInt(f.Size, 10)},
			})
		}
	}
	marker := dir + "/" + bialy.CompleteMarker
	if _, err := os.Stat(filepath.Join(appPath, filepath.FromSlash(marker))); err != nil {
		findings = append(findings, Finding{
			Code: CodeDecideModelMissing, Severity: SeverityError, Path: marker,
			Detail: map[string]string{"reason": "completion marker not found"},
		})
	}
	return findings
}
