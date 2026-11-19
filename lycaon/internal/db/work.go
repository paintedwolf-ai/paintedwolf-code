package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync/atomic"

	"modernc.org/sqlite"
)

// Work counts what store statements did: rows they changed, including rows
// triggers and cascades changed, rows they returned to the engine, and
// database pages they fetched from the page cache or the file.
type Work struct {
	Statements   int64
	Transactions int64
	RowsChanged  int64
	RowsReturned int64
	PagesFetched int64
	// Peaks bound one step: a statement, or a committed transaction.
	PeakStatementRowsChanged   int64
	PeakTransactionRowsChanged int64
	PeakStatementRowsReturned  int64
}

// Counters names each count for performance records.
func (w Work) Counters() map[string]int64 {
	return map[string]int64{
		"statements":                    w.Statements,
		"transactions":                  w.Transactions,
		"rows_changed":                  w.RowsChanged,
		"rows_returned":                 w.RowsReturned,
		"pages_fetched":                 w.PagesFetched,
		"peak_statement_rows_changed":   w.PeakStatementRowsChanged,
		"peak_transaction_rows_changed": w.PeakTransactionRowsChanged,
		"peak_statement_rows_returned":  w.PeakStatementRowsReturned,
	}
}

// WorkTally accumulates Work from concurrent connections.
type WorkTally struct {
	statements, transactions                               atomic.Int64
	rowsChanged, rowsReturned, pagesFetched                atomic.Int64
	peakStatementChanged, peakTxChanged, peakStatementRows atomic.Int64
}

// Work reads the tally.
func (t *WorkTally) Work() Work {
	return Work{
		Statements: t.statements.Load(), Transactions: t.transactions.Load(),
		RowsChanged: t.rowsChanged.Load(), RowsReturned: t.rowsReturned.Load(), PagesFetched: t.pagesFetched.Load(),
		PeakStatementRowsChanged:   t.peakStatementChanged.Load(),
		PeakTransactionRowsChanged: t.peakTxChanged.Load(),
		PeakStatementRowsReturned:  t.peakStatementRows.Load(),
	}
}

func (t *WorkTally) noteStatement(changed, returned, pages int64) {
	t.statements.Add(1)
	t.rowsChanged.Add(changed)
	t.rowsReturned.Add(returned)
	t.pagesFetched.Add(pages)
	raisePeak(&t.peakStatementChanged, changed)
	raisePeak(&t.peakStatementRows, returned)
}

func (t *WorkTally) noteCommit(changed int64) {
	t.transactions.Add(1)
	raisePeak(&t.peakTxChanged, changed)
}

func raisePeak(peak *atomic.Int64, value int64) {
	for {
		current := peak.Load()
		if value <= current || peak.CompareAndSwap(current, value) {
			return
		}
	}
}

// processWork counts every statement on stores this package opens.
var processWork WorkTally

// ProcessWork is the work of every store statement since the process started.
func ProcessWork() Work { return processWork.Work() }

type workTallyKey struct{}

// WithWorkTally attributes statements run with the returned context to tally,
// in addition to the process count.
func WithWorkTally(ctx context.Context, tally *WorkTally) context.Context {
	return context.WithValue(ctx, workTallyKey{}, tally)
}

func contextTally(ctx context.Context) *WorkTally {
	tally, _ := ctx.Value(workTallyKey{}).(*WorkTally)
	return tally
}

// TableWrite is one table a transaction changed and how.
type TableWrite struct {
	Table string
	Op    string
}

// CommitObserver sees each write transaction's table writes as it commits,
// before the commit is durable. Each connection owns one commit hook, which
// the work count holds, so observation rides on it.
type CommitObserver func([]TableWrite)

var preUpdateOps = map[int32]string{9: "delete", 18: "insert", 23: "update"}

// openCounted opens dsn with every statement counted, and its commits
// reported to observe when set.
func openCounted(dsn string, observe CommitObserver) (*sql.DB, error) {
	inner, err := sqlite.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(workConnector{inner: inner, observe: observe}), nil
}

type workConnector struct {
	inner   driver.Connector
	observe CommitObserver
}

func (c workConnector) Driver() driver.Driver { return c.inner.Driver() }

type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
	sqlite.DBStatus
	RegisterPreUpdateHook(sqlite.PreUpdateHookFn)
	RegisterCommitHook(sqlite.CommitHookFn)
	RegisterRollbackHook(sqlite.RollbackHookFn)
	NewBackup(string) (*sqlite.Backup, error)
}

func (c workConnector) Connect(ctx context.Context) (driver.Conn, error) {
	raw, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	inner, ok := raw.(sqliteConn)
	if !ok {
		_ = raw.Close()
		return nil, errors.New("sqlite connection does not expose its work counters")
	}
	conn := &workConn{inner: inner, observe: c.observe}
	inner.RegisterPreUpdateHook(func(d sqlite.SQLitePreUpdateData) {
		conn.changed++
		if conn.observe != nil {
			conn.writes = append(conn.writes, TableWrite{Table: d.TableName, Op: preUpdateOps[d.Op]})
		}
	})
	inner.RegisterCommitHook(func() int32 { conn.settle(true); return 0 })
	inner.RegisterRollbackHook(func() { conn.settle(false) })
	return conn, nil
}

// workConn is used by one goroutine at a time, and SQLite runs its hooks on
// that goroutine, so its fields need no synchronization.
type workConn struct {
	inner sqliteConn
	// changed counts rows this connection changed; settled is its value when
	// the last transaction ended or the open one began.
	changed, settled int64
	// txTally is the open explicit transaction's tally; stmtTally is the
	// running statement's, which an autocommit transaction settles to.
	txTally, stmtTally *WorkTally
	inTx               bool
	observe            CommitObserver
	// writes are the open transaction's table writes, kept only when observed.
	writes []TableWrite
}

func (c *workConn) settle(committed bool) {
	tally := c.stmtTally
	if c.inTx {
		tally = c.txTally
	}
	if committed && c.observe != nil && len(c.writes) > 0 {
		c.observe(c.writes)
	}
	c.writes = c.writes[:0]
	if committed {
		changed := c.changed - c.settled
		processWork.noteCommit(changed)
		if tally != nil {
			tally.noteCommit(changed)
		}
	}
	c.settled, c.inTx, c.txTally = c.changed, false, nil
}

// pages reads and resets the pages this connection fetched since the last read.
func (c *workConn) pages() int64 {
	hits, _, _ := c.inner.Status(sqlite.DBStatusCacheHit, true)
	misses, _, _ := c.inner.Status(sqlite.DBStatusCacheMiss, true)
	return int64(hits) + int64(misses)
}

func (c *workConn) note(tally *WorkTally, changedBefore, returned int64) {
	changed, pages := c.changed-changedBefore, c.pages()
	processWork.noteStatement(changed, returned, pages)
	if tally != nil {
		tally.noteStatement(changed, returned, pages)
	}
}

func (c *workConn) exec(ctx context.Context, run func() (driver.Result, error)) (driver.Result, error) {
	tally, before := contextTally(ctx), c.changed
	c.stmtTally = tally
	result, err := run()
	c.note(tally, before, 0)
	return result, err
}

func (c *workConn) query(ctx context.Context, run func() (driver.Rows, error)) (driver.Rows, error) {
	tally, before := contextTally(ctx), c.changed
	c.stmtTally = tally
	rows, err := run()
	if err != nil {
		c.note(tally, before, 0)
		return nil, err
	}
	return &workRows{inner: rows, conn: c, tally: tally, changedBefore: before}, nil
}

func (c *workConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.exec(ctx, func() (driver.Result, error) { return c.inner.ExecContext(ctx, query, args) })
}

func (c *workConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, func() (driver.Rows, error) { return c.inner.QueryContext(ctx, query, args) })
}

func (c *workConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.inner.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &workStmt{inner: stmt, conn: c}, nil
}

func (c *workConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *workConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.inner.BeginTx(ctx, opts)
	if err == nil {
		c.inTx, c.settled, c.txTally = true, c.changed, contextTally(ctx)
	}
	return tx, err
}

func (c *workConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *workConn) Close() error                                 { return c.inner.Close() }
func (c *workConn) Ping(ctx context.Context) error               { return c.inner.Ping(ctx) }
func (c *workConn) ResetSession(ctx context.Context) error       { return c.inner.ResetSession(ctx) }
func (c *workConn) IsValid() bool                                { return c.inner.IsValid() }
func (c *workConn) NewBackup(dst string) (*sqlite.Backup, error) { return c.inner.NewBackup(dst) }

type workStmt struct {
	inner driver.Stmt
	conn  *workConn
}

func (s *workStmt) Close() error  { return s.inner.Close() }
func (s *workStmt) NumInput() int { return s.inner.NumInput() }

func (s *workStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.conn.exec(ctx, func() (driver.Result, error) {
		return s.inner.(driver.StmtExecContext).ExecContext(ctx, args)
	})
}

func (s *workStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.query(ctx, func() (driver.Rows, error) {
		return s.inner.(driver.StmtQueryContext).QueryContext(ctx, args)
	})
}

func (s *workStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}

func (s *workStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}

func namedValues(args []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, len(args))
	for i, value := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: value}
	}
	return named
}

// workRows counts rows as the engine reads them and settles the statement on
// Close, when every page it fetched has been fetched.
type workRows struct {
	inner         driver.Rows
	conn          *workConn
	tally         *WorkTally
	changedBefore int64
	returned      int64
	closed        bool
}

func (r *workRows) Columns() []string { return r.inner.Columns() }

func (r *workRows) Next(dest []driver.Value) error {
	err := r.inner.Next(dest)
	if err == nil {
		r.returned++
	}
	return err
}

func (r *workRows) Close() error {
	err := r.inner.Close()
	if !r.closed {
		r.closed = true
		r.conn.note(r.tally, r.changedBefore, r.returned)
	}
	return err
}

func (r *workRows) ColumnTypeDatabaseTypeName(index int) string {
	return r.inner.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(index)
}

func (r *workRows) ColumnTypeLength(index int) (int64, bool) {
	return r.inner.(driver.RowsColumnTypeLength).ColumnTypeLength(index)
}

func (r *workRows) ColumnTypeNullable(index int) (bool, bool) {
	return r.inner.(driver.RowsColumnTypeNullable).ColumnTypeNullable(index)
}

func (r *workRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	return r.inner.(driver.RowsColumnTypePrecisionScale).ColumnTypePrecisionScale(index)
}

func (r *workRows) ColumnTypeScanType(index int) reflect.Type {
	return r.inner.(driver.RowsColumnTypeScanType).ColumnTypeScanType(index)
}
