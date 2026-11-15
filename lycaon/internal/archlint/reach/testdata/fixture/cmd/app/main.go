package main

import (
	"context"

	"example.com/fixture/store"
)

func main() {
	ctx := store.WithUser(context.Background(), "u")
	_ = store.User(ctx)
	_ = store.Tenant(ctx)
	store.Save()
	_ = store.Load()
	store.Platform()
}
