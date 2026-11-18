package nativegen

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/pkg/api"
)

type transferState struct {
	sync.Mutex
	active   *transfer
	removing bool
}
type transfer struct {
	cancel       context.CancelFunc
	done         chan struct{}
	target       string
	bytes, total int64
}

func (p *Runtime) SelectionStatus(status *api.LocalGenerationStatus) {
	status.AvailableModels = p.ModelTiers()
	if p == nil || p.config.ModelStore == nil {
		return
	}
	selection, err := p.config.ModelStore.Selection()
	if err != nil {
		status.Detail = err.Error()
		status.State = "unavailable"
		return
	}
	status.SelectedModel = selection.Model
	if selection.State == "opted_out" && status.State != "unavailable" {
		status.State = "opted_out"
		status.RecoveryAction = "none"
	}
	status.SeedModel = p.seedModel()
	status.SeedAvailable = status.SeedModel != ""
	p.transfers.Lock()
	defer p.transfers.Unlock()
	if job := p.transfers.active; job != nil {
		status.State = "downloading"
		status.RecoveryAction = "none"
		status.Detail = ""
		status.TargetModel = job.target
		status.DownloadedBytes = int(job.bytes)
		status.TotalBytes = int(job.total)
	}
}

func (p *Runtime) StartSelection(req api.LocalModelSelectionRequest, installed func(string) error, settled func()) error {
	if p == nil || p.config.ModelStore == nil {
		return errors.New("local generation runtime is unavailable")
	}
	if !KnownModel(req.Model) {
		return errors.New("unknown local model")
	}
	for _, row := range p.ModelTiers() {
		if row.ID == req.Model && !row.Eligible {
			return inference.ErrCapacity
		}
	}
	pin, available := p.pin(req.Model)
	if req.PackagedSeed {
		if req.BundlePath != "" {
			return errors.New("select one offline bundle source")
		}
		if p.seedModel() != req.Model {
			return errors.New("the packaged seed does not match this tier")
		}
		req.BundlePath = p.config.SeedDirectory
	}
	if req.BundlePath != "" && !filepath.IsAbs(req.BundlePath) {
		return errors.New("select an absolute local bundle path")
	}
	if req.BundlePath == "" && !available {
		return errors.New("this model has no published download")
	}
	p.transfers.Lock()
	defer p.transfers.Unlock()
	if p.transfers.active != nil || p.transfers.removing {
		return errors.New("a model transfer is in progress")
	}
	ctx, cancel := context.WithCancel(p.lifetime)
	job := &transfer{cancel: cancel, done: make(chan struct{}), target: req.Model}
	p.transfers.active = job
	go func() {
		defer cancel()
		err := p.importSelection(ctx, req, pin, job)
		if installed != nil {
			err = errors.Join(err, p.config.ModelStore.BindSelectedRoles(installed))
		}
		p.transfers.Lock()
		p.transfers.active = nil
		if err != nil && !errors.Is(err, context.Canceled) {
			p.ReportSelectionError(err)
		}
		close(job.done)
		p.transfers.Unlock()
		settled()
	}()
	return nil
}

func (p *Runtime) importSelection(ctx context.Context, req api.LocalModelSelectionRequest, pin modelstore.ModelPin, job *transfer) error {
	ctx, release, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	p.unload()
	p.publishReadiness("idle", "none")
	progress := func(done, total int64) { p.transfers.Lock(); job.bytes, job.total = done, total; p.transfers.Unlock() }
	if req.BundlePath != "" {
		err = p.config.ModelStore.ImportFromBundlePath(ctx, req.BundlePath, modelstore.Slot{Model: req.Model, Device: p.config.Backend}, progress)
	} else {
		err = p.config.ModelStore.ImportFromBucket(ctx, pin, progress)
	}
	if err != nil {
		return err
	}
	return p.ensureLoaded(ctx, req.Model)
}

func (p *Runtime) CancelTransfer(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.transfers.Lock()
	job := p.transfers.active
	if job != nil {
		job.cancel()
	}
	p.transfers.Unlock()
	if job != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-job.done:
		}
	}
	return nil
}

func (p *Runtime) RemoveModel(ctx context.Context) error {
	if p == nil {
		return errors.New("local generation runtime is unavailable")
	}
	p.transfers.Lock()
	if p.transfers.removing {
		p.transfers.Unlock()
		return errors.New("local model removal is in progress")
	}
	p.transfers.removing = true
	p.transfers.Unlock()
	defer func() { p.transfers.Lock(); p.transfers.removing = false; p.transfers.Unlock() }()
	if err := p.CancelTransfer(ctx); err != nil {
		return err
	}
	ctx, release, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	p.unload()
	if err := p.config.ModelStore.PurgeAll(); err != nil {
		return err
	}
	p.publishReadiness("opted_out", "none")
	return nil
}

func (p *Runtime) SetupInstaller(model string, installed func(string) error, settled func()) {
	fresh, err := p.config.ModelStore.AwaitingInitialModel()
	if err == nil && !fresh {
		return
	}
	if err == nil {
		err = p.StartSelection(api.LocalModelSelectionRequest{Model: model, PackagedSeed: true}, installed, settled)
	}
	if err != nil {
		p.ReportSelectionError(err)
	}
}
