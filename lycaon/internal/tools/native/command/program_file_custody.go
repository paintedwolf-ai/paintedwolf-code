package command

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (r *programRun) lookupCustodyURLs(ctx context.Context, normPaths []string) ([]string, error) {
	var urls []string
	var storeQueryPaths []string
	r.provenance.mu.Lock()
	for _, p := range normPaths {
		if p == "" {
			continue
		}
		if inProg, ok := r.provenance.custodyFiles[p]; ok && len(inProg) > 0 {
			urls = appendUniqueProvenanceStrings(urls, inProg...)
		} else if !r.provenance.clearedFiles[p] {
			urls = appendUniqueProvenanceStrings(urls, r.treeCustodyLocked(p)...)
		}
		if r.provenance.clearedFiles == nil || !r.provenance.clearedFiles[p] {
			storeQueryPaths = appendUniqueProvenanceStrings(storeQueryPaths, p)
		}
	}
	r.provenance.mu.Unlock()

	store := r.custody
	if store == nil || len(storeQueryPaths) == 0 {
		return urls, nil
	}
	projDir := r.projectDir()
	sessionIDs := []string{r.tc.Identity.SessionID}
	if chatID := r.tc.ChatSessionID(); chatID != "" && chatID != r.tc.Identity.SessionID {
		sessionIDs = append(sessionIDs, chatID)
	}
	for _, sid := range sessionIDs {
		if sid == "" {
			continue
		}
		found, err := store.ExternalCustodyURLsForPaths(ctx, sid, projDir, storeQueryPaths)
		if err != nil {
			return nil, err
		}
		urls = appendUniqueProvenanceStrings(urls, found...)
	}
	return urls, nil
}

type nativeFileCustody struct {
	action       string
	sources      []string
	destinations []string
	urls         []string
}

func (r *programRun) prepareNativeFileCustody(ctx context.Context, name string, targets []commandprogram.PathTarget) (nativeFileCustody, error) {
	if r == nil || len(targets) == 0 {
		return nativeFileCustody{}, nil
	}
	plan := nativeFileCustody{action: name}
	switch name {
	case "rm":
		for _, target := range targets {
			plan.sources = append(plan.sources, r.normalizeProvenancePath("", target.Path))
		}
	case "cp", "mv":
		for _, target := range targets {
			path := r.normalizeProvenancePath("", target.Path)
			if path == "" {
				continue
			}
			if target.Write {
				plan.destinations = appendUniqueProvenanceStrings(plan.destinations, path)
			} else {
				plan.sources = appendUniqueProvenanceStrings(plan.sources, path)
			}
		}
		if len(plan.destinations) == 0 && len(targets) >= 2 {
			if last := r.normalizeProvenancePath("", targets[len(targets)-1].Path); last != "" {
				plan.destinations = []string{last}
				plan.sources = nil
				for _, target := range targets[:len(targets)-1] {
					plan.sources = appendUniqueProvenanceStrings(plan.sources, r.normalizeProvenancePath("", target.Path))
				}
			}
		}
		urls, err := r.lookupCustodyURLs(ctx, plan.sources)
		if err != nil {
			return nativeFileCustody{}, err
		}
		plan.urls = urls
	}
	return plan, nil
}

func (p nativeFileCustody) apply(run *programRun) {
	if p.action == "rm" || p.action == "mv" {
		for _, source := range p.sources {
			if !slices.Contains(p.destinations, source) {
				run.clearCustodyFile(source)
			}
		}
	}
	for _, destination := range p.destinations {
		if len(p.urls) > 0 {
			run.addCustodyFile(destination, p.urls)
		} else {
			run.clearCustodyFile(destination)
		}
	}
}

func (r *programRun) invokeWithFileCustody(ctx context.Context, action string, targets []commandprogram.PathTarget, invoke func() (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	plan, err := r.prepareNativeFileCustody(ctx, action, targets)
	if err != nil {
		result := ownerFailure(err)
		result.EffectCertainty = toolcontract.NoEffect
		return result, err
	}
	result, err := invoke()
	if err == nil && result.Status == 0 {
		plan.apply(r)
	}
	return result, err
}
