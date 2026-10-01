package hostcmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"

	lyexec "github.com/lycaon/lycaon/internal/exec"
)

type executionKeyInput struct {
	ProjectDir  string                  `json:"project_dir"`
	ProfileID   string                  `json:"profile_id"`
	Stages      []lyexec.Stage          `json:"stages"`
	Env         []executionKeyEnv       `json:"env,omitempty"`
	Stdin       executionKeyStdin       `json:"stdin,omitempty"`
	Redirect    executionKeyRedirect    `json:"redirect,omitempty"`
	Confinement executionKeyConfinement `json:"confinement,omitempty"`
	// PathExtra changes which program an unqualified name resolves to, so two
	// requests that differ only here are not the same execution.
	PathExtra []string `json:"path_extra,omitempty"`
}

type executionKeyEnv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type executionKeyStdin struct {
	LiteralHash string `json:"literal_hash,omitempty"`
	FromFile    string `json:"from_file,omitempty"`
}

type executionKeyRedirect struct {
	StdoutTo     string             `json:"stdout_to,omitempty"`
	StdoutAppend bool               `json:"stdout_append,omitempty"`
	StderrTo     string             `json:"stderr_to,omitempty"`
	StderrAppend bool               `json:"stderr_append,omitempty"`
	Files        []executionKeyFile `json:"files,omitempty"`
}

type executionKeyFile struct {
	Path     string `json:"path"`
	Resolved string `json:"resolved"`
}

type executionKeyConfinement struct {
	Roots     []string `json:"roots,omitempty"`
	ReadRoots []string `json:"read_roots,omitempty"`
	Network   int      `json:"network,omitempty"`
	Browser   bool     `json:"browser,omitempty"`
}

// ExecutionKey returns a stable identity for one logical command execution request.
// It includes command inputs and the effective confinement boundary while excluding
// per-call proxy addresses and attribution tokens. Literal stdin is hashed rather than
// exposed to diagnostics or process-ledger surfaces.
func ExecutionKey(req Request) string {
	in := executionKeyInput{
		ProjectDir: req.ProjectDir,
		ProfileID:  req.ProfileID,
		Stages:     append([]lyexec.Stage(nil), req.Stages...),
		PathExtra:  append([]string(nil), req.PathExtra...),
	}
	keys := make([]string, 0, len(req.InlineEnv))
	for key := range req.InlineEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		in.Env = append(in.Env, executionKeyEnv{Key: key, Value: req.InlineEnv[key]})
	}
	if req.Stdin != nil {
		if req.Stdin.From != nil {
			in.Stdin.FromFile = filepath.Join(req.Stdin.From.Root, req.Stdin.From.Rel)
		}
		if len(req.Stdin.Literal) > 0 {
			sum := sha256.Sum256(req.Stdin.Literal)
			in.Stdin.LiteralHash = hex.EncodeToString(sum[:])
		}
	}
	if req.Redirect != nil {
		if out := req.Redirect.Stdout; out != nil {
			in.Redirect.StdoutTo = filepath.Join(out.Location.Root, out.Location.Rel)
			in.Redirect.StdoutAppend = out.Append
		}
		if errOut := req.Redirect.Stderr; errOut != nil {
			in.Redirect.StderrTo = filepath.Join(errOut.Location.Root, errOut.Location.Rel)
			in.Redirect.StderrAppend = errOut.Append
		}
		paths := make([]string, 0, len(req.Redirect.Files))
		for path := range req.Redirect.Files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			loc := req.Redirect.Files[path]
			in.Redirect.Files = append(in.Redirect.Files, executionKeyFile{Path: path, Resolved: filepath.Join(loc.Root, loc.Rel)})
		}
	}
	if req.Launch.Confinement != nil {
		in.Confinement = executionKeyConfinement{
			Roots:     append([]string(nil), req.Launch.Confinement.Roots...),
			ReadRoots: append([]string(nil), req.Launch.Confinement.ReadRoots...),
			Network:   int(req.Launch.Confinement.Network), Browser: req.Launch.Confinement.Browser,
		}
		sort.Strings(in.Confinement.Roots)
		sort.Strings(in.Confinement.ReadRoots)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
