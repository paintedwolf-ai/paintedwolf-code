package persistence

// Verify refuses store-coupled deletion after claim loss or integrity failure.
func (r *Runtime) Verify() error {
	if err := r.Database.Failure(); err != nil {
		return err
	}
	if r.Claim != nil {
		return r.Claim.Verify()
	}
	return nil
}
