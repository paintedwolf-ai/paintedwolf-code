package fseffect

import "fmt"

// Replacement holds a reviewed stream and its parent descriptors until its one
// publication attempt or abandonment. The caller must Close it on every path.
// A Replacement is confined to one caller and is not safe for concurrent use.
type Replacement struct {
	result    Result
	publish   func() (Result, error)
	close     func()
	attempted bool
}

// StageReplace writes and reviews bytes without publishing the destination.
// The result also describes bytes written when staging fails.
func StageReplace(req ReplaceRequest) (*Replacement, Result, error) {
	return stageReplace(req, nil)
}

// Commit revalidates and publishes the staged stream once.
func (r *Replacement) Commit() (Result, error) {
	if r == nil || r.close == nil || r.attempted {
		return Result{}, fmt.Errorf("replacement is no longer publishable")
	}
	r.attempted = true
	result, err := r.publish()
	r.result = result
	return result, err
}

// Close abandons an unpublished stream and releases held capabilities.
func (r *Replacement) Close() {
	if r == nil || r.close == nil {
		return
	}
	r.close()
	r.close, r.publish = nil, nil
}

func replace(req ReplaceRequest, inject func(stage) error) (Result, error) {
	staged, result, err := stageReplace(req, inject)
	if err != nil {
		return result, err
	}
	defer staged.Close()
	return staged.Commit()
}
