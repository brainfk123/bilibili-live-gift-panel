package runtime

import (
	"context"
	"errors"
	"sync"
	"time"
)

type LeaseKind string

const (
	LeaseConfig LeaseKind = "config"
	LeaseOBS    LeaseKind = "obs"
)

var ErrInvalidLease = errors.New("runtime: invalid lease")

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemTimer struct{ timer *time.Timer }

func newSystemTimer(delay time.Duration) Timer { return systemTimer{timer: time.NewTimer(delay)} }
func (timer systemTimer) C() <-chan time.Time  { return timer.timer.C }
func (timer systemTimer) Stop() bool           { return timer.timer.Stop() }

type Lease struct {
	manager   *Manager
	accountID int64
	id        uint64
	kind      LeaseKind
	once      sync.Once
}

type ConnectionLease interface {
	Kind() LeaseKind
	Renew(context.Context) error
	Release()
}

func (lease *Lease) Kind() LeaseKind {
	if lease == nil {
		return ""
	}
	return lease.kind
}

func (lease *Lease) Renew(ctx context.Context) error {
	if lease == nil || lease.manager == nil || ctx == nil {
		return ErrInvalidLease
	}
	return lease.manager.renew(ctx, lease)
}

func (lease *Lease) Release() {
	if lease == nil || lease.manager == nil {
		return
	}
	lease.once.Do(func() { lease.manager.release(lease) })
}

func validLeaseKind(kind LeaseKind) bool { return kind == LeaseConfig || kind == LeaseOBS }
