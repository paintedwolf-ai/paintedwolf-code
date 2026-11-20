package project

import "context"

func createWithRoot(ctx context.Context, reg Registry, path string) (*Project, error) {
	return reg.Create(ctx, CreateParams{Roots: []AttachRootParams{{Path: path}}})
}
