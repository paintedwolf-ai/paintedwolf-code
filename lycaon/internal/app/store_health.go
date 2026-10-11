package app

func (a *ServeApp) storeFailed() <-chan struct{} {
	if a.resources == nil {
		return nil
	}
	return a.resources.db.Failed()
}

func (a *ServeApp) storeFailure() error {
	if a.resources == nil {
		return nil
	}
	return a.resources.db.Failure()
}
