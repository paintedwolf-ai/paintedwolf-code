package bundled

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
)

const consumerRecordName = "consumer-finalization.json"
const maxConsumerRecordBytes = 1 << 20

// ConsumerIdentity distinguishes the producer executable from final packaged bytes.
type ConsumerIdentity struct {
	ProducerSHA256 string          `json:"producer_sha256"`
	ProducerBytes  int64           `json:"producer_bytes"`
	Record         PayloadIdentity `json:"record"`
}

func (consumer *ConsumerIdentity) validate(identity *BuildIdentity) error {
	if identity.GOOS != "linux" || !validHex(consumer.ProducerSHA256, 32) || consumer.ProducerSHA256 == identity.BinarySHA256 || consumer.ProducerBytes <= 0 || consumer.ProducerBytes > maxArtifactBytes {
		return fmt.Errorf("consumer finalization requires distinct Linux producer and packaged identities")
	}
	record := consumer.Record
	if record.Name != consumerRecordName || !validHex(record.SHA256, 32) || record.Bytes <= 0 || record.Bytes > maxConsumerRecordBytes {
		return fmt.Errorf("consumer finalization record identity is invalid")
	}
	return nil
}

func artifactPayloads(m *Manifest) []PayloadIdentity {
	parts := append([]PayloadIdentity{}, m.identity.Payload...)
	if m.identity.Consumer != nil {
		parts = append(parts, m.identity.Consumer.Record)
	}
	return parts
}

// ConsumerRecipe attributes declared packaging inputs separately from observed bytes.
type ConsumerRecipe struct {
	SchemaVersion   int      `json:"schema_version"`
	Kind            string   `json:"kind"`
	SourceCommit    string   `json:"source_commit"`
	FlakeLockSHA256 string   `json:"flake_lock_sha256"`
	ToolPath        string   `json:"tool_path"`
	ToolSHA256      string   `json:"tool_sha256"`
	Arguments       []string `json:"arguments"`
}

// ParseConsumerRecipe reads one closed declaration of consumer packaging inputs.
func ParseConsumerRecipe(raw []byte) (ConsumerRecipe, error) {
	var recipe ConsumerRecipe
	if len(raw) > 64<<10 {
		return recipe, fmt.Errorf("consumer recipe exceeds its bound")
	}
	if err := rejectVerifierDuplicateFields(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return recipe, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&recipe); err != nil {
		return recipe, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return recipe, fmt.Errorf("consumer recipe requires one JSON object")
	}
	return recipe, recipe.validate()
}

func (recipe ConsumerRecipe) validate() error {
	if recipe.SchemaVersion != 1 || (recipe.Kind != "nix-elf-relocation" && recipe.Kind != "nix-nuitka-relocation") || !validHex(recipe.SourceCommit, 20) || !validHex(recipe.FlakeLockSHA256, 32) || !validHex(recipe.ToolSHA256, 32) {
		return fmt.Errorf("consumer recipe requires a versioned Nix relocation and pinned source inputs")
	}
	if !filepath.IsAbs(recipe.ToolPath) || filepath.Clean(recipe.ToolPath) != recipe.ToolPath || len(recipe.Arguments) == 0 || len(recipe.Arguments) > 128 {
		return fmt.Errorf("consumer recipe requires explicit tool and argument identities")
	}
	for _, argument := range recipe.Arguments {
		if len(argument) > 4096 || bytes.IndexByte([]byte(argument), 0) >= 0 {
			return fmt.Errorf("consumer recipe argument exceeds its bound")
		}
	}
	return nil
}

// ConsumerPlatform records the final executable's observed ELF deployment surface.
type ConsumerPlatform struct {
	Machine     string   `json:"machine"`
	Interpreter string   `json:"interpreter"`
	Needed      []string `json:"needed"`
	RPaths      []string `json:"rpaths"`
}

type consumerExecutable struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type consumerRecord struct {
	SchemaVersion int                `json:"schema_version"`
	Producer      *BuildIdentity     `json:"producer_build_identity"`
	Recipe        ConsumerRecipe     `json:"declared_recipe"`
	Executable    consumerExecutable `json:"final_executable"`
	Platform      ConsumerPlatform   `json:"observed_platform"`
	Qualification json.RawMessage    `json:"consumer_qualification"`
}

func readConsumerRecord(m *Manifest, directory string) ([]byte, error) {
	part := m.identity.Consumer.Record
	raw, err := readCandidateMetadata(filepath.Join(directory, part.Name))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != part.Bytes || candidateDigest(raw) != part.SHA256 {
		return nil, fmt.Errorf("consumer finalization record differs from embedded authority")
	}
	return raw, nil
}

func verifyConsumerRecord(m *Manifest, directory string) error {
	raw, err := readConsumerRecord(m, directory)
	if err != nil {
		return err
	}
	if err := rejectVerifierDuplicateFields(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return err
	}
	var record consumerRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("consumer finalization requires one JSON record")
	}
	producer := record.Producer
	identity := m.identity
	if record.SchemaVersion != 1 || producer == nil || producer.SchemaVersion != 1 || producer.Consumer != nil {
		return fmt.Errorf("consumer finalization must preserve an original producer identity")
	}
	if err := producer.validate(m.OpenGrep); err != nil {
		return err
	}
	if producer.GOOS != identity.GOOS || producer.GOARCH != identity.GOARCH || producer.BinarySHA256 != identity.Consumer.ProducerSHA256 || producer.BinaryBytes != identity.Consumer.ProducerBytes || !reflect.DeepEqual(producer.Payload, identity.Payload) {
		return fmt.Errorf("consumer finalization changed original producer authority")
	}
	if m.consumerSource != nil && !reflect.DeepEqual(producer, m.consumerSource.identity) {
		return fmt.Errorf("consumer record differs from the independently admitted producer")
	}
	if record.Executable != (consumerExecutable{GOOS: identity.GOOS, GOARCH: identity.GOARCH, SHA256: identity.BinarySHA256, Bytes: identity.BinaryBytes}) {
		return fmt.Errorf("consumer finalization does not bind the final packaged executable")
	}
	if err := record.Recipe.validate(); err != nil {
		return err
	}
	platform, err := inspectConsumerELF(filepath.Join(directory, "opengrep"), identity.GOARCH)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(platform, record.Platform) {
		return fmt.Errorf("consumer platform observation differs from final executable")
	}
	_, err = projectConsumerQualification(record.Qualification, identity)
	return err
}
