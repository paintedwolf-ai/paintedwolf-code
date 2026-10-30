package survey

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func deliverCredentialText(ctx context.Context, reads *projectpaths.ReadSession, tc tools.ToolContext, path, text string) error {
	if text == "" || !protectedpath.IsCredentialFile(path) {
		return nil
	}
	resolved, err := reads.Resolve(ctx, path)
	if err != nil {
		return err
	}
	return tc.ObserveCredentialDelivery(ctx, resolved.Abs, resolved.DisplayPath, text)
}

func deliverGrepCredentials(ctx context.Context, search *grepSearch, response grepResponse) error {
	for _, match := range response.Matches {
		text := strings.Join(append(append(append([]string(nil), match.ContextBefore...), match.Content), match.ContextAfter...), "\n")
		if err := deliverCredentialText(ctx, search.reads, search.tctx, match.Path, text); err != nil {
			return err
		}
	}
	for _, highlight := range response.Highlights {
		if err := deliverCredentialText(ctx, search.reads, search.tctx, highlight.Path, highlight.Content); err != nil {
			return err
		}
	}
	return nil
}

func deliverSummaryCredentials(ctx context.Context, reads *projectpaths.ReadSession, tc tools.ToolContext, result summarize.Result) error {
	for _, window := range result.Pack.Substance {
		if err := deliverCredentialText(ctx, reads, tc, window.Path, window.Body); err != nil {
			return err
		}
	}
	for _, site := range result.Pack.CallSites {
		if err := deliverCredentialText(ctx, reads, tc, site.Path, site.Excerpt); err != nil {
			return err
		}
	}
	for _, anchor := range result.Anchors {
		if err := deliverCredentialText(ctx, reads, tc, anchor.Path, anchor.Excerpt); err != nil {
			return err
		}
	}
	for _, sample := range result.Gather.SampleMatches {
		if err := deliverCredentialText(ctx, reads, tc, sample.Path, sample.Content); err != nil {
			return err
		}
	}
	return nil
}
