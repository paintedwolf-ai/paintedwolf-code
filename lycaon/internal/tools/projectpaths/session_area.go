package projectpaths

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// resolveSessionArea answers virtual and absolute names under the current
// session's host directories. Output is a read namespace, never a write root.
func resolveSessionArea(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, bool, error) {
	operand := modelPath
	backing, mapped := tools.PrivateTempOperand(tctx, modelPath)
	if mapped {
		operand = backing
	}
	area, rel, dir, addressed := sessionRel(tctx, operand)
	if mapped && (area != projectroot.VirtualScratchLabel || !addressed) {
		return Resolved{}, false, projectroot.ErrPathEscape
	}
	if !addressed {
		return Resolved{}, false, nil
	}
	if dir == "" {
		code := tools.SessionScratchUnavailableCode
		if area == projectroot.VirtualOutputLabel {
			code = tools.SessionOutputUnavailableCode
		}
		facts := map[string]any{"path": modelPath}
		if code == tools.SessionScratchUnavailableCode {
			facts["private_temp"] = confine.Enforcing() && len(confine.PrivateTempFor("").Paths) > 0
		}
		return Resolved{}, false, &toolrejection.ToolReject{Code: code, Data: facts}
	}
	if area == projectroot.VirtualOutputLabel && op != sandbox.PathOpRead {
		return Resolved{}, false, projectroot.ErrPathEscape
	}
	abs, err := projectroot.SessionPath(dir, rel)
	if err != nil {
		return Resolved{}, false, err
	}
	if !confine.PathAtOrUnder(fspath.CanonicalPath(abs), fspath.CanonicalPath(dir)) {
		return Resolved{}, false, projectroot.ErrPathEscape
	}
	scopeRel, err := filepath.Rel(filepath.Clean(dir), abs)
	if err != nil {
		return Resolved{}, false, err
	}
	scopeRel = filepath.ToSlash(scopeRel)
	display := "@" + area
	if scopeRel == "." {
		scopeRel = ""
	} else {
		display += "/" + scopeRel
	}
	if mapped {
		display = modelPath
	}
	if b != nil {
		if op == sandbox.PathOpRead {
			err = b.AssertReadScope(ctx, dir, scopeRel, tctx.ProfileID())
		} else {
			err = b.AssertPathAllowed(ctx, dir, scopeRel, op)
		}
		if err != nil {
			return Resolved{}, false, err
		}
	}
	rootID, compressed, toolOutput := area, false, area == projectroot.VirtualOutputLabel
	if host := strings.TrimSpace(tctx.Host.HostDataDir); toolOutput && host != "" && dir == filepath.Clean(host) && tooloutput.IsAgentWireSpillRel(scopeRel) {
		// Spill files keep the host-data identity their bare spelling uses.
		rootID = hostDataRootID
		compressed = tooloutput.IsBlobstoreCompressedRel(scopeRel)
		toolOutput = strings.HasPrefix(scopeRel, tooloutput.ToolOutputSpillDir+"/") ||
			strings.HasPrefix(scopeRel, tooloutput.PromoteSpillDir+"/")
	}
	return Resolved{Abs: abs, DisplayPath: display, ScopeRel: scopeRel, Root: projectroot.RootRef{ID: rootID, Label: area, Path: filepath.Clean(dir)}, External: true, Compressed: compressed, ToolOutput: toolOutput}, true, nil
}

// sessionRel selects the host directory that owns an operand's exact path.
func sessionRel(tc tools.ToolContext, path string) (area, rel, dir string, ok bool) {
	if area, rel, ok = projectroot.SessionAddress(path); ok {
		dir = tc.Host.SessionScratchDir
		if area == projectroot.VirtualOutputLabel {
			dir = tc.Host.SessionOutputDir
			// Host spill paths share the @output namespace so spill files
			// resolve and display through one spelling.
			if host := strings.TrimSpace(tc.Host.HostDataDir); host != "" && tooloutput.IsAgentWireSpillRel(rel) {
				dir = filepath.Clean(host)
			}
		}
		return
	}
	for _, entry := range []struct{ area, dir string }{{projectroot.VirtualOutputLabel, tc.Host.SessionOutputDir}, {projectroot.VirtualScratchLabel, tc.Host.SessionScratchDir}} {
		if rel, ok = sessionRelOfAbs(strings.TrimSpace(entry.dir), path); ok {
			return entry.area, rel, entry.dir, true
		}
	}
	return "", "", "", false
}

// sessionRelOfAbs returns an absolute path's place inside the scratch folder,
// comparing canonical forms so an alias of the folder still resolves there.
func sessionRelOfAbs(dir, raw string) (string, bool) {
	// Operand whitespace is filename data.
	if dir == "" || !filepath.IsAbs(raw) {
		return "", false
	}
	rel, err := filepath.Rel(fspath.CanonicalPath(dir), fspath.CanonicalPath(raw))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
