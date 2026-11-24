package sessions

import "context"

// ForgetProject removes the project's roots and its retained file evidence
// after any in-flight pass has stopped publishing.
func (c *credentialFiles) ForgetProject(ctx context.Context, project string) error {
	if !c.indexing() {
		return nil
	}
	i := c.index
	ctx, finish, err := i.work.Begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	i.mu.Lock()
	var keys []string
	for _, root := range i.roots {
		if root.projectID == project {
			keys = append(keys, root.key())
			i.detach(root)
		}
	}
	i.mu.Unlock()
	for _, key := range keys {
		release, err := i.access.Acquire(ctx, key)
		if err != nil {
			return err
		}
		defer release()
	}
	return c.harvest.ForgetProject(project)
}
