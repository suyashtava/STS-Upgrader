// Package nodepoollease implements expiring leases for node pools.
//
// A lease prevents contention for a given node pool (and it's subsequent
// nodes and pods). By limiting activity to a single controller or operator
// at a time, we can avoid accidental availability disruptions.
//
// A requester can acquire a lease for a specific node pool. If the holder
// of the lease needs additional time, it can renew the lease. If the lease
// is not actively renewed, it will eventually expire. If another requester
// tries to acquire the same lease, the request will be blocked until the lease
// has expired.
//
// https://salesforce.quip.com/aCt8AytN8VNa
package nodepoollease

import (
	"bytes"
	"context"
	"sync"
	"time"

	"git.soma.salesforce.com/kubernetes/kube-node-recycler/internal/metrics"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	rl "k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/clock"
)

const (
	JitterFactor    = 1.2
	LeaseNamePrefix = "nodepool-"
)

func NewNodePoolLease(nplc NodePoolLeaseConfig, logger logrus.FieldLogger) (*NodePoolLease, error) {
	npl := NodePoolLease{
		config: nplc,
		clock:  clock.RealClock{},
		// 	metrics: globalMetricsFactory.newLeaderMetrics(),
		logger: logger,
	}
	// npl.metrics.leaderOff(npl.config.Name)
	return &npl, nil
}

// Return default NodePoolLease Config
func NewDefaultNodePoolLeaseConfig(client coordinationv1client.LeasesGetter, nodePoolName string, nodePoolNamespace string, holderID string) (*NodePoolLeaseConfig, error) {
	var lock = &rl.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      LeaseNamePrefix + nodePoolName,
			Namespace: nodePoolNamespace,
		},
		Client: client, // CoordinationV1(),
		LockConfig: rl.ResourceLockConfig{
			Identity: holderID,
		},
	}

	nplc := NodePoolLeaseConfig{
		Lock:              lock,
		ReleaseOnCancel:   true,
		LeaseDuration:     48 * time.Hour,   // how long the process will work
		RenewDeadline:     47 * time.Hour,   // overall time the leader has to renew lease
		RetryPeriod:       60 * time.Second, // when renew will occur
		MaxAutoRenewCount: 0,
	}
	return &nplc, nil
}

// Return NodePoolLeaseConfig with passed in parameters
func NewNodePoolLeaseConfig(client coordinationv1client.LeasesGetter, leaseName string, nodePoolNamespace string, holderID string, leaseDuration time.Duration, renewDeadline time.Duration, retryPeriod time.Duration, maxAutoRenewCount int) (*NodePoolLeaseConfig, error) {
	var lock = &rl.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseName,
			Namespace: nodePoolNamespace,
		},
		Client: client, // CoordinationV1(),
		LockConfig: rl.ResourceLockConfig{
			Identity: holderID,
		},
	}

	nplc := NodePoolLeaseConfig{
		Lock:              lock,
		ReleaseOnCancel:   true,
		LeaseDuration:     leaseDuration, // how long the process will work
		RenewDeadline:     renewDeadline, // overall time the leader has to renew lease
		RetryPeriod:       retryPeriod,   // when renew will occur
		MaxAutoRenewCount: maxAutoRenewCount,
	}
	return &nplc, nil
}

type NodePoolLeaseConfig struct {
	Lock          rl.Interface
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
	Callbacks     NodePoolLeaseCallbacks
	// WatchDog *HealthzAdaptor
	ReleaseOnCancel   bool
	Name              string
	MaxAutoRenewCount int
}

type NodePoolLeaseCallbacks struct {
	OnStartedLeading func(context.Context)
	OnStoppedLeading func()
	OnNewLeader      func(identity string)
}

type NodePoolLease struct {
	config            NodePoolLeaseConfig
	observedRecord    rl.LeaderElectionRecord
	observedRawRecord []byte
	observedTime      time.Time
	// used to implement OnNewLeader(), may lag slightly from the
	// value observedRecord.HolderIdentity if the transition has
	// not yet been reported.
	reportedLeader string

	// clock is wrapper around time to allow for less flaky testing
	clock clock.Clock

	// used to lock the observedRecord
	observedRecordLock sync.Mutex

	// metrics leaderMetricsAdapter
	observedRenewCount int

	logger logrus.FieldLogger
}

// IsLeader returns true if the last observed leader was this client else returns false.
func (npl *NodePoolLease) IsLeader() bool {
	return npl.getObservedRecord().HolderIdentity == npl.config.Lock.Identity()
}

// Try to acquire nodepool lease
// 1. if lease doesn't exist or it's already the current holder, it will acquire or renew lease
// 2. if lease exists and it's not the current holder, it will block until expiration
func (npl *NodePoolLease) Acquire(ctx context.Context) bool {
	ctx, cancel := context.WithCancel(ctx)

	defer cancel()
	succeeded := false
	desc := npl.config.Lock.Describe()

	npl.logger.Debugf("attempting to acquire lease %v...", desc)
	wait.JitterUntil(func() {
		succeeded = npl.tryAcquireOrRenew(ctx)
		npl.maybeReportTransition()
		if !succeeded {
			metrics.RecyclerLeaseAvailable.Set(0)
			npl.logger.Errorf("failed to acquire lease %v", desc)
			return
		}

		npl.config.Lock.RecordEvent("became leader")
		// npl.metrics.leaderOn(npl.config.Name)
		metrics.RecyclerLeaseAvailable.Set(1)
		npl.logger.Infof("successfully acquired lease %v, observed time %s, release time %s", desc, npl.observedTime, npl.observedTime.Add(time.Second*time.Duration(npl.observedRecord.LeaseDurationSeconds)))
		cancel()
	}, npl.config.RetryPeriod, JitterFactor, true, ctx.Done())
	return succeeded
}

// Release attempts to release the leader lease if we have acquired it.
func (npl *NodePoolLease) Release() bool {
	if !npl.IsLeader() {
		return true
	}
	now := metav1.Now()
	leaderElectionRecord := rl.LeaderElectionRecord{
		LeaderTransitions:    npl.observedRecord.LeaderTransitions,
		LeaseDurationSeconds: 1,
		RenewTime:            now,
		AcquireTime:          now,
	}
	desc := npl.config.Lock.Describe()
	if err := npl.config.Lock.Update(context.TODO(), leaderElectionRecord); err != nil {
		npl.logger.Errorf("Failed to release lease: %v", err)
		return false
	}
	npl.logger.Debugf("successfully released lease %v", desc)
	npl.observedRenewCount = 0
	npl.setObservedRecord(&leaderElectionRecord)
	return true
}

// Release attempts to release the leader lease if we have acquired it. Additional buffer (in seconds) added to avoid flapping
func (npl *NodePoolLease) ReleaseAfterDuration(leaseDurationSeconds int) bool {
	if !npl.IsLeader() {
		return true
	}
	now := metav1.Now()
	leaderElectionRecord := rl.LeaderElectionRecord{
		LeaderTransitions:    npl.observedRecord.LeaderTransitions,
		LeaseDurationSeconds: leaseDurationSeconds,
		RenewTime:            now,
		AcquireTime:          now,
	}
	if err := npl.config.Lock.Update(context.TODO(), leaderElectionRecord); err != nil {
		npl.logger.Errorf("Failed to release lease: %v", err)
		return false
	}
	desc := npl.config.Lock.Describe()
	npl.observedRenewCount = 0
	npl.setObservedRecord(&leaderElectionRecord)
	npl.logger.Debugf("successfully released lease %v, observed time %s, release time %s ", desc, npl.observedTime, npl.observedTime.Add(time.Second*time.Duration(npl.observedRecord.LeaseDurationSeconds)))

	return true
}

// Try to acquire or renew a lease by checking for existing lease record
func (npl *NodePoolLease) tryAcquireOrRenew(ctx context.Context) bool {
	now := metav1.Now()
	leaderElectionRecord := rl.LeaderElectionRecord{
		HolderIdentity:       npl.config.Lock.Identity(),
		LeaseDurationSeconds: int(npl.config.LeaseDuration / time.Second),
		RenewTime:            now,
		AcquireTime:          now,
	}

	// 1. obtain or create the ElectionRecord
	oldLeaderElectionRecord, oldLeaderElectionRawRecord, err := npl.config.Lock.Get(ctx)
	if err != nil {
		if !errors.IsNotFound(err) {
			npl.logger.Errorf("error retrieving resource lease %v: %v", npl.config.Lock.Describe(), err)
			return false
		}
		if err = npl.config.Lock.Create(ctx, leaderElectionRecord); err != nil {
			npl.logger.Errorf("error initially creating leader election record: %v", err)
			return false
		}
		npl.setObservedRecord(&leaderElectionRecord)

		return true
	}

	// 2. Record obtained, check the Identity & Time
	if !bytes.Equal(npl.observedRawRecord, oldLeaderElectionRawRecord) {
		npl.setObservedRecord(oldLeaderElectionRecord)

		npl.observedRawRecord = oldLeaderElectionRawRecord
	}
	leaseExpireTime := npl.observedRecord.RenewTime.Add(time.Duration(npl.observedRecord.LeaseDurationSeconds) * time.Second)
	if len(oldLeaderElectionRecord.HolderIdentity) > 0 &&
		leaseExpireTime.After(now.Time) &&
		!npl.IsLeader() {
		npl.logger.Infof("lease is held by %v and has not yet expired %s", oldLeaderElectionRecord.HolderIdentity, leaseExpireTime)
		return false
	}

	// 3. We're going to try to update. The leaderElectionRecord is set to it's default
	// here. Let's correct it before updating.
	if npl.IsLeader() {
		leaderElectionRecord.AcquireTime = oldLeaderElectionRecord.AcquireTime
		leaderElectionRecord.LeaderTransitions = oldLeaderElectionRecord.LeaderTransitions
	} else {
		leaderElectionRecord.LeaderTransitions = oldLeaderElectionRecord.LeaderTransitions + 1
	}

	// update the lock itself
	if err = npl.config.Lock.Update(ctx, leaderElectionRecord); err != nil {
		npl.logger.Errorf("Failed to update lease: %v", err)
		return false
	}

	npl.setObservedRecord(&leaderElectionRecord)
	return true

}

// update transition field in lease
func (npl *NodePoolLease) maybeReportTransition() {
	if npl.observedRecord.HolderIdentity == npl.reportedLeader {
		return
	}
	npl.reportedLeader = npl.observedRecord.HolderIdentity
	if npl.config.Callbacks.OnNewLeader != nil {
		go npl.config.Callbacks.OnNewLeader(npl.reportedLeader)
	}
}

// set lease record
func (npl *NodePoolLease) setObservedRecord(observedRecord *rl.LeaderElectionRecord) {
	npl.observedRecordLock.Lock()
	defer npl.observedRecordLock.Unlock()

	npl.observedRecord = *observedRecord
	npl.observedTime = npl.clock.Now()
}

// get lease record
func (npl *NodePoolLease) getObservedRecord() rl.LeaderElectionRecord {
	npl.observedRecordLock.Lock()
	defer npl.observedRecordLock.Unlock()

	return npl.observedRecord
}
