package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

type dbAdvisoryLockLease struct {
	conn   *sql.Conn
	lockID int64

	releaseOnce sync.Once
	releaseErr  error
}

func hashAdvisoryLockID(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64())
}

func tryAcquireDBAdvisoryLock(ctx context.Context, db *sql.DB, lockID int64) (func(), bool) {
	release, acquired, _ := tryAcquireDBAdvisoryLockWithError(ctx, db, lockID)
	return release, acquired
}

func tryAcquireDBAdvisoryLockWithError(ctx context.Context, db *sql.DB, lockID int64) (func(), bool, error) {
	lease, acquired, err := tryAcquireDBAdvisoryLockLease(ctx, db, lockID)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return func() { _ = lease.Release() }, true, nil
}

func tryAcquireDBAdvisoryLockLease(ctx context.Context, db *sql.DB, lockID int64) (*dbAdvisoryLockLease, bool, error) {
	if db == nil {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("open advisory-lock connection: %w", err)
	}

	acquired := false
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired); err != nil {
		// The server may have acquired the session lock before the client failed
		// to read the result. Discard the physical connection instead of returning
		// an ambiguously locked session to the pool.
		discardSQLConn(conn)
		return nil, false, fmt.Errorf("query advisory lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}

	return &dbAdvisoryLockLease{conn: conn, lockID: lockID}, true, nil
}

func (l *dbAdvisoryLockLease) Ping(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return fmt.Errorf("advisory-lock connection is unavailable")
	}
	return l.conn.PingContext(ctx)
}

func (l *dbAdvisoryLockLease) Release() error {
	if l == nil {
		return nil
	}
	l.releaseOnce.Do(func() {
		if l.conn == nil {
			return
		}
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		unlocked := false
		if err := l.conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", l.lockID).Scan(&unlocked); err != nil {
			l.releaseErr = fmt.Errorf("unlock advisory lock: %w", err)
			discardSQLConn(l.conn)
			return
		}
		if !unlocked {
			l.releaseErr = fmt.Errorf("advisory lock %d was not held by its lease connection", l.lockID)
			discardSQLConn(l.conn)
			return
		}
		if err := l.conn.Close(); err != nil {
			l.releaseErr = fmt.Errorf("close advisory-lock connection: %w", err)
		}
	})
	return l.releaseErr
}

func discardSQLConn(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
