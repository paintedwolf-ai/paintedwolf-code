package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Source view unions reject cross-kind fields at the wire boundary.
type SourceViewCreate struct {
	Tree       *SourceTreeViewCreate       `json:"-"`
	Comparison *SourceComparisonViewCreate `json:"-"`
}

func (v SourceViewCreate) Validate() error {
	count := 0
	if v.Tree != nil {
		count++
		if v.Tree.Kind != "tree" {
			return fmt.Errorf("SourceViewCreate has an inconsistent discriminator")
		}
	}
	if v.Comparison != nil {
		count++
		if v.Comparison.Kind != "comparison" {
			return fmt.Errorf("SourceViewCreate has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceViewCreate requires exactly one kind")
	}
	return nil
}
func (v SourceViewCreate) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Tree != nil {
		return json.Marshal(v.Tree)
	}
	if v.Comparison != nil {
		return json.Marshal(v.Comparison)
	}
	return nil, fmt.Errorf("SourceViewCreate is empty")
}
func (v *SourceViewCreate) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceViewCreate
	switch tag.Kind {
	case "tree":
		var part SourceTreeViewCreate
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Tree = &part
	case "comparison":
		var part SourceComparisonViewCreate
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Comparison = &part
	default:
		return fmt.Errorf("unknown SourceViewCreate kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}

type SourceView struct {
	Tree       *SourceTreeView       `json:"-"`
	Comparison *SourceComparisonView `json:"-"`
}

func (v SourceView) Validate() error {
	count := 0
	if v.Tree != nil {
		count++
		if v.Tree.Kind != "tree" {
			return fmt.Errorf("SourceView has an inconsistent discriminator")
		}
	}
	if v.Comparison != nil {
		count++
		if v.Comparison.Kind != "comparison" {
			return fmt.Errorf("SourceView has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceView requires exactly one kind")
	}
	return nil
}
func (v SourceView) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Tree != nil {
		return json.Marshal(v.Tree)
	}
	if v.Comparison != nil {
		return json.Marshal(v.Comparison)
	}
	return nil, fmt.Errorf("SourceView is empty")
}
func (v *SourceView) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceView
	switch tag.Kind {
	case "tree":
		var part SourceTreeView
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Tree = &part
	case "comparison":
		var part SourceComparisonView
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Comparison = &part
	default:
		return fmt.Errorf("unknown SourceView kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}

type SourceViewUpdate struct {
	Tree       *SourceTreeViewUpdate       `json:"-"`
	Comparison *SourceComparisonViewUpdate `json:"-"`
}

func (v SourceViewUpdate) Validate() error {
	count := 0
	if v.Tree != nil {
		count++
		if v.Tree.Kind != "tree" {
			return fmt.Errorf("SourceViewUpdate has an inconsistent discriminator")
		}
	}
	if v.Comparison != nil {
		count++
		if v.Comparison.Kind != "comparison" {
			return fmt.Errorf("SourceViewUpdate has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceViewUpdate requires exactly one kind")
	}
	return nil
}
func (v SourceViewUpdate) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Tree != nil {
		return json.Marshal(v.Tree)
	}
	if v.Comparison != nil {
		return json.Marshal(v.Comparison)
	}
	return nil, fmt.Errorf("SourceViewUpdate is empty")
}
func (v *SourceViewUpdate) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceViewUpdate
	switch tag.Kind {
	case "tree":
		var part SourceTreeViewUpdate
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Tree = &part
	case "comparison":
		var part SourceComparisonViewUpdate
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Comparison = &part
	default:
		return fmt.Errorf("unknown SourceViewUpdate kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}

type SourceViewFrame struct {
	Tree       *SourceTreeFrame       `json:"-"`
	Comparison *SourceComparisonFrame `json:"-"`
}

func (v SourceViewFrame) Validate() error {
	count := 0
	if v.Tree != nil {
		count++
		if v.Tree.Kind != "tree" {
			return fmt.Errorf("SourceViewFrame has an inconsistent discriminator")
		}
	}
	if v.Comparison != nil {
		count++
		if v.Comparison.Kind != "comparison" {
			return fmt.Errorf("SourceViewFrame has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceViewFrame requires exactly one kind")
	}
	return nil
}
func (v SourceViewFrame) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Tree != nil {
		return json.Marshal(v.Tree)
	}
	if v.Comparison != nil {
		return json.Marshal(v.Comparison)
	}
	return nil, fmt.Errorf("SourceViewFrame is empty")
}
func (v *SourceViewFrame) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceViewFrame
	switch tag.Kind {
	case "tree":
		var part SourceTreeFrame
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Tree = &part
	case "comparison":
		var part SourceComparisonFrame
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Comparison = &part
	default:
		return fmt.Errorf("unknown SourceViewFrame kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}

type SourceViewLocation struct {
	Tree       *SourceTreeLocation       `json:"-"`
	Comparison *SourceComparisonLocation `json:"-"`
}

func (v SourceViewLocation) Validate() error {
	count := 0
	if v.Tree != nil {
		count++
		if v.Tree.Kind != "tree" {
			return fmt.Errorf("SourceViewLocation has an inconsistent discriminator")
		}
	}
	if v.Comparison != nil {
		count++
		if v.Comparison.Kind != "comparison" {
			return fmt.Errorf("SourceViewLocation has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceViewLocation requires exactly one kind")
	}
	return nil
}
func (v SourceViewLocation) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Tree != nil {
		return json.Marshal(v.Tree)
	}
	if v.Comparison != nil {
		return json.Marshal(v.Comparison)
	}
	return nil, fmt.Errorf("SourceViewLocation is empty")
}
func (v *SourceViewLocation) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceViewLocation
	switch tag.Kind {
	case "tree":
		var part SourceTreeLocation
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Tree = &part
	case "comparison":
		var part SourceComparisonLocation
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Comparison = &part
	default:
		return fmt.Errorf("unknown SourceViewLocation kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}

type SourceViewSearchPage struct {
	Tree       *SourceTreeSearchPage       `json:"-"`
	Comparison *SourceComparisonSearchPage `json:"-"`
}

func (v SourceViewSearchPage) Validate() error {
	count := 0
	if v.Tree != nil {
		count++
		if v.Tree.Kind != "tree" {
			return fmt.Errorf("SourceViewSearchPage has an inconsistent discriminator")
		}
	}
	if v.Comparison != nil {
		count++
		if v.Comparison.Kind != "comparison" {
			return fmt.Errorf("SourceViewSearchPage has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceViewSearchPage requires exactly one kind")
	}
	return nil
}
func (v SourceViewSearchPage) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Tree != nil {
		return json.Marshal(v.Tree)
	}
	if v.Comparison != nil {
		return json.Marshal(v.Comparison)
	}
	return nil, fmt.Errorf("SourceViewSearchPage is empty")
}
func (v *SourceViewSearchPage) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceViewSearchPage
	switch tag.Kind {
	case "tree":
		var part SourceTreeSearchPage
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Tree = &part
	case "comparison":
		var part SourceComparisonSearchPage
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Comparison = &part
	default:
		return fmt.Errorf("unknown SourceViewSearchPage kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}

type SourceComparisonSelector struct {
	Current   *CurrentComparisonSource   `json:"-"`
	Effect    *EffectComparisonSource    `json:"-"`
	Version   *VersionComparisonSource   `json:"-"`
	Scope     *ScopeComparisonSource     `json:"-"`
	Turn      *TurnComparisonSource      `json:"-"`
	Reviewed  *ReviewedComparisonSource  `json:"-"`
	Commit    *CommitComparisonSource    `json:"-"`
	Blob      *BlobComparisonSource      `json:"-"`
	GitChange *GitChangeComparisonSource `json:"-"`
	GitRange  *GitRangeComparisonSource  `json:"-"`
	Chat      *ChatComparisonSource      `json:"-"`
	Text      *TextComparisonSource      `json:"-"`
	Retained  *RetainedComparisonSource  `json:"-"`
}

func (v SourceComparisonSelector) Validate() error {
	count := 0
	if v.Current != nil {
		count++
		if v.Current.Kind != "current" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Effect != nil {
		count++
		if v.Effect.Kind != "effect" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Version != nil {
		count++
		if v.Version.Kind != "version" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Scope != nil {
		count++
		if v.Scope.Kind != "scope" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Turn != nil {
		count++
		if v.Turn.Kind != "turn" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Reviewed != nil {
		count++
		if v.Reviewed.Kind != "reviewed" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Commit != nil {
		count++
		if v.Commit.Kind != "commit" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Blob != nil {
		count++
		if v.Blob.Kind != "blob" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.GitChange != nil {
		count++
		if v.GitChange.Kind != "git_change" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.GitRange != nil {
		count++
		if v.GitRange.Kind != "git_range" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Chat != nil {
		count++
		if v.Chat.Kind != "chat" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Text != nil {
		count++
		if v.Text.Kind != "text" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if v.Retained != nil {
		count++
		if v.Retained.Kind != "retained" {
			return fmt.Errorf("SourceComparisonSelector has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceComparisonSelector requires exactly one kind")
	}
	return nil
}
func (v SourceComparisonSelector) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Current != nil {
		return json.Marshal(v.Current)
	}
	if v.Effect != nil {
		return json.Marshal(v.Effect)
	}
	if v.Version != nil {
		return json.Marshal(v.Version)
	}
	if v.Scope != nil {
		return json.Marshal(v.Scope)
	}
	if v.Turn != nil {
		return json.Marshal(v.Turn)
	}
	if v.Reviewed != nil {
		return json.Marshal(v.Reviewed)
	}
	if v.Commit != nil {
		return json.Marshal(v.Commit)
	}
	if v.Blob != nil {
		return json.Marshal(v.Blob)
	}
	if v.GitChange != nil {
		return json.Marshal(v.GitChange)
	}
	if v.GitRange != nil {
		return json.Marshal(v.GitRange)
	}
	if v.Chat != nil {
		return json.Marshal(v.Chat)
	}
	if v.Text != nil {
		return json.Marshal(v.Text)
	}
	if v.Retained != nil {
		return json.Marshal(v.Retained)
	}
	return nil, fmt.Errorf("SourceComparisonSelector is empty")
}
func (v *SourceComparisonSelector) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceComparisonSelector
	switch tag.Kind {
	case "current":
		var part CurrentComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Current = &part
	case "effect":
		var part EffectComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Effect = &part
	case "version":
		var part VersionComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Version = &part
	case "scope":
		var part ScopeComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Scope = &part
	case "turn":
		var part TurnComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Turn = &part
	case "reviewed":
		var part ReviewedComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Reviewed = &part
	case "commit":
		var part CommitComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Commit = &part
	case "blob":
		var part BlobComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Blob = &part
	case "git_change":
		var part GitChangeComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.GitChange = &part
	case "git_range":
		var part GitRangeComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.GitRange = &part
	case "chat":
		var part ChatComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Chat = &part
	case "text":
		var part TextComparisonSource
		if err := decodeSourceViewVariant(data, &part, "before", "after"); err != nil {
			return err
		}
		out.Text = &part
	case "retained":
		var part RetainedComparisonSource
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Retained = &part
	default:
		return fmt.Errorf("unknown SourceComparisonSelector kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}

type SourceTreeCommand struct {
	Toggle   *SourceTreeToggle   `json:"-"`
	Disclose *SourceTreeDisclose `json:"-"`
	Reveal   *SourceTreeReveal   `json:"-"`
	Filter   *SourceTreeFilter   `json:"-"`
	Review   *SourceTreeReview   `json:"-"`
}

func (v SourceTreeCommand) Validate() error {
	count := 0
	if v.Toggle != nil {
		count++
		if v.Toggle.Kind != "toggle" {
			return fmt.Errorf("SourceTreeCommand has an inconsistent discriminator")
		}
	}
	if v.Disclose != nil {
		count++
		if v.Disclose.Kind != "disclose" {
			return fmt.Errorf("SourceTreeCommand has an inconsistent discriminator")
		}
	}
	if v.Reveal != nil {
		count++
		if v.Reveal.Kind != "reveal" {
			return fmt.Errorf("SourceTreeCommand has an inconsistent discriminator")
		}
	}
	if v.Filter != nil {
		count++
		if v.Filter.Kind != "filter" {
			return fmt.Errorf("SourceTreeCommand has an inconsistent discriminator")
		}
	}
	if v.Review != nil {
		count++
		if v.Review.Kind != "review" {
			return fmt.Errorf("SourceTreeCommand has an inconsistent discriminator")
		}
	}
	if count != 1 {
		return fmt.Errorf("SourceTreeCommand requires exactly one kind")
	}
	return nil
}
func (v SourceTreeCommand) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Toggle != nil {
		return json.Marshal(v.Toggle)
	}
	if v.Disclose != nil {
		return json.Marshal(v.Disclose)
	}
	if v.Reveal != nil {
		return json.Marshal(v.Reveal)
	}
	if v.Filter != nil {
		return json.Marshal(v.Filter)
	}
	if v.Review != nil {
		return json.Marshal(v.Review)
	}
	return nil, fmt.Errorf("SourceTreeCommand is empty")
}
func (v *SourceTreeCommand) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	var out SourceTreeCommand
	switch tag.Kind {
	case "toggle":
		var part SourceTreeToggle
		if err := decodeSourceViewVariant(data, &part, "address"); err != nil {
			return err
		}
		out.Toggle = &part
	case "disclose":
		var part SourceTreeDisclose
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Disclose = &part
	case "reveal":
		var part SourceTreeReveal
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Reveal = &part
	case "filter":
		var part SourceTreeFilter
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Filter = &part
	case "review":
		var part SourceTreeReview
		if err := decodeSourceViewVariant(data, &part); err != nil {
			return err
		}
		out.Review = &part
	default:
		return fmt.Errorf("unknown SourceTreeCommand kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*v = out
	return nil
}
func decodeSourceViewVariant(data []byte, destination any, required ...string) error {
	if len(required) > 0 {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(data, &members); err != nil {
			return err
		}
		for _, name := range required {
			if _, exists := members[name]; !exists {
				return fmt.Errorf("source view field %s is required", name)
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}
