package sqliteretry

import (
	"context"
	"errors"
	"math/rand"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	"time"
)

func IsBusy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}
func WithBusyRetry(ctx context.Context, op func() error) error {
	backoff := []time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	for n := 0; ; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op()
		if err == nil {
			return nil
		}
		if !IsBusy(err) || n == len(backoff) {
			return err
		}
		d := backoff[n] + time.Duration(rand.Int63n(int64(backoff[n])/4+1))
		timer := time.NewTimer(d)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}
