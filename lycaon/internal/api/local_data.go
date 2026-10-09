package api

import (
	"context"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/branchretention"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *LocalData) localDataRegistry() (*localdata.Registry, error) {
	if s.localData != nil {
		return s.localData, nil
	}
	reg, err := localdata.New(s.dataDir)
	if err != nil {
		return nil, err
	}
	reg.SetWebIndexClear(s.clearWebIndex)
	reg.SetSourceObservationsClear(s.sourceLedger.Snapshots.ClearObservations)
	reg.SetSourceCatalogClear(sourcecatalog.Process().Trees.ClearTreeStores)
	reg.SetSourceCatalogSpilled(sourcecatalog.SpilledBytes)
	reg.SetExtensionCacheClear(s.extensionOwner.ClearCache)
	reg.SetSessionScratchReclaim(s.sessions.Runner.Execution.ReclaimScratch)
	if s.workerBranchRoot != "" {
		deps := s.branchRetentionDeps()
		reg.SetWorkerBranches(
			func(ctx context.Context) (bool, int64, error) { return branchretention.Inventory(ctx, deps.BranchRoot) },
			func(ctx context.Context) error {
				_, err := branchretention.ReclaimSealed(ctx, deps)
				return err
			},
		)
	}
	s.localData = reg
	return reg, nil
}

// branchRetentionDeps binds branch retention to this server's queue and root.
func (s *LocalData) branchRetentionDeps() branchretention.Deps {
	return branchretention.Deps{
		BranchRoot: s.workerBranchRoot,
		Jobs: func(ctx context.Context) (map[string]branchretention.JobState, error) {
			jobs, err := s.workers.ListBranchJobs(ctx)
			if err != nil {
				return nil, err
			}
			out := make(map[string]branchretention.JobState, len(jobs))
			for _, job := range jobs {
				out[job.ID] = branchretention.JobState{Sealed: job.Sealed}
			}
			return out, nil
		},
		Evict: workspace.EvictJobTree,
	}
}

func (s *LocalData) clearWebIndex(ctx context.Context) error {
	if s.webIndex == nil {
		return nil
	}
	return s.webIndex.Clear(ctx)
}

func (s *LocalData) handleGetLocalData(w http.ResponseWriter, r *http.Request) {
	reg, err := s.localDataRegistry()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	rows, err := reg.StatusAll(r.Context())
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := wire.LocalDataStatus{
		Buckets: make([]wire.LocalDataBucketStatus, 0, len(rows)), WorkspaceCaches: []wire.WorkspaceCacheStatus{},
	}
	for _, row := range rows {
		st := wire.LocalDataBucketStatus{
			ID:      wire.LocalDataBucketId(row.ID),
			Present: row.Present,
		}
		if row.Bytes > 0 {
			st.Bytes = row.Bytes
		}
		out.Buckets = append(out.Buckets, st)
	}
	caches, err := workspace.ListSeedCaches(r.Context(), s.workerSeedRoot)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	for _, cache := range caches {
		out.WorkspaceCaches = append(out.WorkspaceCaches, wire.WorkspaceCacheStatus{
			ID: cache.ID, SourceRoot: cache.SourceRoot, LogicalBytes: cache.LogicalBytes,
			AllocatedBytes: cache.AllocatedBytes, LastUsedAt: cache.LastUsed,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *LocalData) handleClearLocalData(w http.ResponseWriter, r *http.Request) {
	var req wire.LocalDataClearRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if len(req.Buckets) == 0 && len(req.WorkspaceCacheIDs) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "buckets or workspace_cache_ids required")
		return
	}
	ids := make([]string, 0, len(req.Buckets))
	for _, id := range req.Buckets {
		if !localdata.Known(string(id)) {
			s.responses.InvalidField(w, "buckets", "names an unknown bucket")
			return
		}
		ids = append(ids, string(id))
	}
	reg, err := s.localDataRegistry()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	var results []localdata.ClearResult
	if len(ids) > 0 {
		results, err = reg.ClearBuckets(r.Context(), ids)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	out := wire.LocalDataClearResponse{
		Results:               make([]wire.LocalDataClearResult, 0, len(results)),
		WorkspaceCacheResults: make([]wire.WorkspaceCacheClearResult, 0, len(req.WorkspaceCacheIDs)),
	}
	for _, res := range results {
		clearRes := wire.LocalDataClearResult{
			ID: wire.LocalDataBucketId(res.ID),
			OK: res.OK,
		}
		if !res.OK {
			s.responses.Logger.WarnContext(r.Context(), "local data bucket clear failed", "bucket", res.ID, "err", res.Error)
			clearRes.Code = wire.ApiErrorCodeInternalError
			clearRes.Message = "this data could not be cleared"
		}
		out.Results = append(out.Results, clearRes)
	}
	for _, id := range req.WorkspaceCacheIDs {
		err := workspace.RemoveSeedByID(r.Context(), s.workerSeedRoot, id)
		result := wire.WorkspaceCacheClearResult{ID: id, OK: err == nil}
		if err != nil {
			s.responses.Logger.WarnContext(r.Context(), "worker cache clear failed", "workspace_cache_id", id, "err", err)
			result.Code = wire.ApiErrorCodeInternalError
			result.Message = "this worker cache could not be cleared"
		}
		out.WorkspaceCacheResults = append(out.WorkspaceCacheResults, result)
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}
