package sourceapi

import "context"

// Stop releases project routing before its host stores are shut down.
func (s *Watch) Stop() {
	if s == nil {
		return
	}
	s.watchWork.Stop()
	s.watches.Stop()
}

// Wait joins watch installation and copied observer delivery before store release.
func (s *Watch) Wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if err := s.watchWork.Wait(ctx); err != nil {
		return err
	}
	return s.watches.Wait(ctx)
}
