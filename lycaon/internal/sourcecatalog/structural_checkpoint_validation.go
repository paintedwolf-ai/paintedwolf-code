package sourcecatalog

import (
	"context"
	"io"
	"math"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

type structuralCheckpointPage struct {
	branch     pagedview.Branch
	last       string
	parent     string
	references uint8
	height     int
}

type structuralCheckpointDecoder struct {
	builder       *structuralBuilder
	workspace     *structuralCheckpointWorkspace
	directories   int64
	lastDirectory string
	mark          func(string) uint64
	epoch         repochange.Epoch
}

func (decoder *structuralCheckpointDecoder) records(ctx context.Context, reader io.Reader, directoryCount int64) error {
	for {
		kind, body, err := readStructuralFrame(ctx, reader)
		if err != nil {
			return err
		}
		switch kind {
		case 'P':
			if err := decoder.page(ctx, body); err != nil {
				return err
			}
		case 'D':
			if decoder.directories >= directoryCount {
				return pagedview.ErrRange
			}
			if err := decoder.directory(ctx, body); err != nil {
				return err
			}
		case 0:
			if len(body) != 0 || decoder.directories != directoryCount {
				return pagedview.ErrRange
			}
			return decoder.workspace.allReferenced(ctx)
		default:
			return pagedview.ErrRange
		}
	}
}

func (decoder *structuralCheckpointDecoder) page(ctx context.Context, body []byte) error {
	page, err := decodeRangePage(body)
	if err != nil {
		return err
	}
	facts, err := decoder.pageFacts(ctx, &page)
	if err != nil {
		return err
	}
	facts.branch.Page, err = decoder.builder.Write(ctx, 0, page)
	if err != nil {
		return err
	}
	_, err = decoder.workspace.addFact(ctx, facts)
	return err
}

func (decoder *structuralCheckpointDecoder) pageFacts(ctx context.Context, page *pagedview.RangePage[TreeItem]) (structuralCheckpointPage, error) {
	facts := structuralCheckpointPage{height: 1}
	for _, item := range page.Items {
		name := path.Base(item.Value.Path)
		if !validStructuralPath(item.Value.Path) || item.Value.Path == "." || item.Key != DirectoryOrder(name, directoryOrderKind(item.Key)) {
			return facts, pagedview.ErrRange
		}
		parent := path.Dir(item.Value.Path)
		if err := facts.add(pagedview.Branch{Key: item.Key, Weight: item.Weight, Count: 1, Unresolved: item.Unresolved, Fingerprint: item.Fingerprint, BaselineFingerprint: item.BaselineFingerprint}, item.Key, parent); err != nil {
			return facts, err
		}
	}
	for i := range page.Children {
		child := &page.Children[i]
		local := child.Page
		known, err := decoder.workspace.fact(ctx, local)
		if err != nil {
			return facts, err
		}
		expected := known.branch
		expected.Page = local
		if expected != *child || known.references != 0 || known.height >= 64 {
			return facts, pagedview.ErrRange
		}
		if err := decoder.workspace.reference(ctx, local); err != nil {
			return facts, err
		}
		if err := facts.add(*child, known.last, known.parent); err != nil {
			return facts, err
		}
		facts.height = max(facts.height, known.height+1)
		child.Page = known.branch.Page
	}
	return facts, nil
}

func (facts *structuralCheckpointPage) add(branch pagedview.Branch, last, parent string) error {
	if branch.Key == "" || last < branch.Key || facts.last != "" && facts.last >= branch.Key || facts.parent != "" && facts.parent != parent {
		return pagedview.ErrRange
	}
	if branch.Weight < 0 || branch.Weight > math.MaxInt64-facts.branch.Weight || branch.Count < 0 || branch.Count > math.MaxInt64-facts.branch.Count || branch.Unresolved < 0 || branch.Unresolved > math.MaxInt64-facts.branch.Unresolved {
		return pagedview.ErrWeight
	}
	if facts.branch.Key == "" {
		facts.branch.Key = branch.Key
	}
	facts.last, facts.parent = last, parent
	facts.branch.Weight += branch.Weight
	facts.branch.Count += branch.Count
	facts.branch.Unresolved += branch.Unresolved
	facts.branch.Fingerprint = facts.branch.Fingerprint.Combine(branch.Fingerprint)
	facts.branch.BaselineFingerprint = facts.branch.BaselineFingerprint.Combine(branch.BaselineFingerprint)
	return nil
}

func (decoder *structuralCheckpointDecoder) directory(ctx context.Context, body []byte) error {
	var record structuralCheckpointDirectory
	if err := decodeStructuralJSON(body, &record); err != nil {
		return err
	}
	observation := record.Observation
	observation.Path = string(record.Path)
	if record.Observation.Path != "" || !validStructuralPath(observation.Path) || observation.Sequence < 0 || observation.FirstListed < 0 || observation.Entries < 0 {
		return pagedview.ErrRange
	}
	if observation.FirstListed > observation.Sequence || observation.Complete && observation.FirstListed == 0 || record.Page != 0 && observation.FirstListed == 0 {
		return pagedview.ErrRange
	}
	if decoder.mark != nil {
		observation.Invalidation = decoder.mark(observation.Path)
	}
	if decoder.epoch.BootID != "" {
		observation.Epoch = decoder.epoch
	}
	if decoder.directories > 0 && decoder.lastDirectory >= observation.Path {
		return pagedview.ErrRange
	}
	if record.Page != 0 {
		local := record.Page
		page, err := decoder.workspace.fact(ctx, local)
		if err != nil || page.references != 0 || page.parent != "" && page.parent != observation.Path {
			return pagedview.ErrRange
		}
		if err := decoder.workspace.reference(ctx, local); err != nil {
			return err
		}
		record.Page = page.branch.Page
	}
	directory := structuralDirectory{page: record.Page, observation: observation}
	if err := decoder.builder.directories.Set(ctx, observation.Path, directory); err != nil {
		return err
	}
	decoder.directories++
	decoder.lastDirectory = observation.Path
	return nil
}

func validStructuralPath(value string) bool {
	return value != "" && !strings.ContainsRune(value, 0) && value != ".." && !strings.HasPrefix(value, "../") && !strings.HasPrefix(value, "/") && path.Clean(value) == value
}

func validateStructuralDirectoryItems(ctx context.Context, builder *structuralBuilder) error {
	return builder.directories.Visit(ctx, func(directory structuralDirectory) error {
		index := pagedview.RangeIndex[TreeItem]{Store: builder, Root: directory.page}
		after := ""
		for {
			items, err := index.ReadAfter(ctx, after, indexBatchSize)
			if err != nil || len(items) == 0 {
				return err
			}
			for _, item := range items {
				node := indexNode{path: item.Value.Path, name: path.Base(item.Value.Path), isDir: directoryOrderKind(item.Key), isSymlink: item.Value.Symlink}
				want, err := builder.item(ctx, node)
				if err != nil {
					return err
				}
				want.Value.Sequence = item.Value.Sequence
				if item != want || item.Value.Sequence > directory.observation.Sequence {
					return pagedview.ErrRange
				}
			}
			after = items[len(items)-1].Key
		}
	})
}
