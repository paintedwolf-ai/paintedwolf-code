package store

import "context"

type userKey struct{}

type tenantKey struct{}

// WithUser is set and read.
func WithUser(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userKey{}, id)
}

func User(ctx context.Context) string { s, _ := ctx.Value(userKey{}).(string); return s }

// Tenant reads a key only a test sets.
func Tenant(ctx context.Context) string { s, _ := ctx.Value(tenantKey{}).(string); return s }

// WithTenant is reached by no program.
func WithTenant(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, tenantKey{}, id)
}

const insertAudit = "INSERT INTO audit (id) VALUES (?)"
const pruneEvents = "DELETE FROM events WHERE id IN (SELECT id FROM archived)"
const listItems = "SELECT id FROM items JOIN tags ON tags.item = items.id"

func exec(string) {}

func Save() { exec(insertAudit); exec(pruneEvents) }

func Load() string { exec(listItems); return "" }

type sealed interface{ sealed() }

type impl struct{}

func (impl) sealed() {}

type Repository interface {
	Put()
	Get() string
}

type liveRepo struct{}

func NewLiveRepo() Repository { return &liveRepo{} }

func (*liveRepo) Put()        {}
func (*liveRepo) Get() string { return "" }

type memoryRepo struct{}

func (*memoryRepo) Put()        {}
func (*memoryRepo) Get() string { return "" }
func (*memoryRepo) Extra()      {}
