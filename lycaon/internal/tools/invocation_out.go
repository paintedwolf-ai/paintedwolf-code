package tools

import (
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// MergeScope names the seam a child's outputs cross.
type MergeScope int

const (
	// MergeStageOK forwards a completed stage's feedback and confinement.
	MergeStageOK MergeScope = iota
	// MergeStageFailed forwards only what a failed stage already did.
	MergeStageFailed
	// MergeProgram forwards a program's whole outcome to its call.
	MergeProgram
)

// MaxSourceDeliveryTags bounds a result's tags. A wide search would otherwise print
// one for every match; its records stay citable by path.
const MaxSourceDeliveryTags = 16

// MergeChild moves a child's outputs onto o. Effects that already happened
// always cross, advisory facts cross by scope, and fields that describe the
// child's own call never do.
func (o *ToolInvocationOut) MergeChild(child *ToolInvocationOut, scope MergeScope) {
	if o == nil || child == nil || o == child {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	child.mu.Lock()
	defer child.mu.Unlock()
	for _, edit := range child.FileEdits {
		o.RecordFileEdit(edit)
	}
	o.FileEditsOmitted += child.FileEditsOmitted
	if context := child.SourceContext; context != nil {
		for _, target := range context.Locations {
			o.recordSourceLocation(target)
		}
		if context.Truncated {
			o.truncateSourceContext()
		}
	}
	o.SourceReads = append(o.SourceReads, child.SourceReads...)
	for _, tag := range child.SourceDeliveries {
		o.addSourceDelivery(tag)
	}
	if child.Listener != nil {
		o.Listener = child.Listener
	}
	if child.ExternalAccess != nil {
		o.ExternalAccess = child.ExternalAccess
	}
	switch scope {
	case MergeStageOK:
		for _, item := range child.Facts.Feedback {
			o.Facts = o.Facts.WithFeedback(item.Code, item.Details, item.Subject)
		}
		o.Facts.Confine = o.Facts.Confine.Merge(child.Facts.Confine)
	case MergeProgram:
		o.Facts = o.Facts.Merge(child.Facts)
	case MergeStageFailed:
	}
}

// TakeCommitted drains the effects recorded so far into a new value.
func (o *ToolInvocationOut) TakeCommitted() *ToolInvocationOut {
	taken := &ToolInvocationOut{}
	if o == nil {
		return taken
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	taken.FileEdits, o.FileEdits = o.FileEdits, nil
	taken.FileEditsOmitted, o.FileEditsOmitted = o.FileEditsOmitted, 0
	taken.SourceContext, o.SourceContext = o.SourceContext, nil
	taken.sourceLocations, o.sourceLocations = o.sourceLocations, nil
	taken.SourceReads, o.SourceReads = o.SourceReads, nil
	taken.SourceDeliveries, o.SourceDeliveries = o.SourceDeliveries, nil
	taken.Listener, o.Listener = o.Listener, nil
	taken.ExternalAccess, o.ExternalAccess = o.ExternalAccess, nil
	return taken
}

// WithLock runs write while no merge or take can observe o. Writers that share
// o across goroutines use it; a stage's own outputs have one writer.
func (o *ToolInvocationOut) WithLock(write func(*ToolInvocationOut)) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	write(o)
}

// AddSourceDelivery records one tag per handle, up to MaxSourceDeliveryTags.
func (o *ToolInvocationOut) AddSourceDelivery(tag SourceDeliveryTag) {
	o.WithLock(func(o *ToolInvocationOut) { o.addSourceDelivery(tag) })
}

func (o *ToolInvocationOut) addSourceDelivery(tag SourceDeliveryTag) {
	if len(o.SourceDeliveries) >= MaxSourceDeliveryTags {
		return
	}
	for _, seen := range o.SourceDeliveries {
		if seen.Handle == tag.Handle {
			return
		}
	}
	o.SourceDeliveries = append(o.SourceDeliveries, tag)
}

func (o *ToolInvocationOut) recordSourceLocation(target api.NavigationTarget) {
	if o.SourceContext != nil && o.SourceContext.Truncated {
		return
	}
	key := sourceref.Key(target)
	if _, exists := o.sourceLocations[key]; exists {
		return
	}
	if o.SourceContext == nil {
		o.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{}}
	}
	if len(o.SourceContext.Locations) >= api.MaxSourceContextLocations {
		o.SourceContext.Truncated = true
		return
	}
	if o.sourceLocations == nil {
		o.sourceLocations = make(map[string]struct{})
	}
	o.sourceLocations[key] = struct{}{}
	o.SourceContext.Locations = append(o.SourceContext.Locations, target)
}

func (o *ToolInvocationOut) truncateSourceContext() {
	if o.SourceContext == nil {
		o.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{}}
	}
	o.SourceContext.Truncated = true
}
