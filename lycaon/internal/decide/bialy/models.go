package bialy

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
)

// ModelFile is one pinned file of a checkpoint.
type ModelFile struct {
	// Path is the file's location under the checkpoint directory.
	Path string
	// SHA256 is the pinned hex digest.
	SHA256 string
	// Size is the byte length.
	Size int64
}

// Model is a pinned upstream checkpoint on the Hugging Face Hub.
type Model struct {
	// ID is the hub repository, as receipts name the model.
	ID string
	// Revision is the commit the pins were taken from.
	Revision string
	Files    []ModelFile
}

// ShippedModel pins the backbone checkpoint used to train the bundled heads.
var ShippedModel = Model{
	ID:       "convaiinnovations/laya-multilingual",
	Revision: "e4e9ddf21a7b1903b7acffd8814ad4307bf63a67",
	Files: []ModelFile{
		{Path: "rl_agent_config.json", SHA256: "25061739243b617ad88d1219ba6f8a9c86c5881ca28df024fa2d9b3b2fcc30c6", Size: 472},
		{Path: "model.safetensors", SHA256: "9d628fd971b700382ac6f65920a86f149777b2e748e0c955fb3b19695aa8f204", Size: 643835514},
		{Path: "encoder/config.json", SHA256: "83f6916d13ef0f556ac461f28308dc2bffa7ebeadee8ec9e2db5812020ea5bb4", Size: 1938},
		{Path: "tokenizer/tokenizer.json", SHA256: "609d8f4c067cd3950f88594c5a802616cea245823836ef5848ee4fc40aab5b6f", Size: 34363188},
		{Path: "tokenizer/tokenizer_config.json", SHA256: "424b69444bf7b5809dc2cd2e36d0bd71b8055124dd24274d6db3c655d38205e7", Size: 502},
	},
}

// DirName is the checkpoint's directory name, keyed by revision.
func (m Model) DirName() string {
	return strings.ReplaceAll(m.ID, "/", "--") + "@" + m.Revision[:12]
}

// BundledRel is the checkpoint's slash-separated directory under the engine root.
func (m Model) BundledRel() string {
	return modelsDir + "/" + m.DirName()
}

// ManagedModelDir is where the checkpoint is provisioned outside a packaged app, or ""
// without a config dir.
func ManagedModelDir() string {
	base, err := configdir.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(enginepaths.DecideModelsRootUnder(base), ShippedModel.DirName())
}
