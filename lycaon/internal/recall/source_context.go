package recall

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *Service) attachSourceContexts(ctx context.Context, sources []search.Hit, hits []Hit) {
	contexts := map[string]*api.SourceContext{}
	for i, source := range sources {
		if source.MessageID == "" || source.SessionID == "" {
			continue
		}
		key := source.SessionID + "\x00" + source.MessageID
		sourceContext, loaded := contexts[key]
		if !loaded {
			contexts[key] = nil
			var err error
			sourceContext, err = sourceref.ReadContext(ctx, s.db, source.SessionID, source.MessageID)
			if err != nil {
				continue
			}

			contexts[key] = sourceContext
		}
		hits[i].SourceContext = sourceref.Mentioned(sourceContext, hits[i].Path+"\n"+hits[i].Snippet+"\n"+strings.Join(hits[i].Body, "\n"))
	}
}
