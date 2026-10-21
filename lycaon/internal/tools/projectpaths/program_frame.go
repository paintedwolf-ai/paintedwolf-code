package projectpaths

// Fork returns an independent directory frame starting at p's current cwd, so
// cd inside a subshell, pipeline stage or job never reaches the parent.
func (p *ProgramPaths) Fork() *ProgramPaths {
	fork := *p
	return &fork
}
