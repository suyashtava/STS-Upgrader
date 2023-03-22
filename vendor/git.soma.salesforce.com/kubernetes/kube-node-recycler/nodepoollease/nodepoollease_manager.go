package nodepoollease

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/utils/strings/slices"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

type NodePoolLeaseManager struct {
	Logger                logrus.FieldLogger
	LeaseNamespace        string
	LeaseEnabledNodepools []string
	Client                coordinationv1client.LeasesGetter
	LeaseNodePoolsMap     map[string]*NodePoolLease
}

// Return a new NodePoolLeaseManager, given a `namespace` and list of participating `nodePools`
func NewNodePoolLeaseManager(logger logrus.FieldLogger, client coordinationv1client.LeasesGetter, namespace string, nodePools []string) *NodePoolLeaseManager {
	return &NodePoolLeaseManager{
		LeaseNamespace:        namespace,
		LeaseEnabledNodepools: nodePools,
		Client:                client,
		Logger:                logger,
		LeaseNodePoolsMap:     make(map[string]*NodePoolLease),
	}
}

// Return a new NodePoolLease with default config
func (m *NodePoolLeaseManager) CreateDefaultNodePoolLease(nodePool string, leaseHolderID string) (*NodePoolLease, error) {

	// new lease
	nplc, err := NewDefaultNodePoolLeaseConfig(m.Client, nodePool, m.LeaseNamespace, leaseHolderID)
	if err != nil {
		return nil, fmt.Errorf("failed to configure lease: %w", err)
	}
	m.Logger.Debugf("successfully configured new lease")
	npl, err := NewNodePoolLease(*nplc, m.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create lease: %w", err)
	}

	return npl, nil
}

// Return a new NodePoolLease with custom config
func (m *NodePoolLeaseManager) CreateNodePoolLease(leaseName string, leaseHolderID string, leaseDuration time.Duration, renewDeadline time.Duration, retryPeriod time.Duration, maxAutoRenewCount int) (*NodePoolLease, error) {

	// new lease
	nplc, err := NewNodePoolLeaseConfig(m.Client, leaseName, m.LeaseNamespace, leaseHolderID, leaseDuration, renewDeadline, retryPeriod, maxAutoRenewCount)
	if err != nil {
		return nil, fmt.Errorf("failed to configure lease: %w", err)
	}
	m.Logger.Debugf("successfully configured new lease")
	npl, err := NewNodePoolLease(*nplc, m.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create lease: %w", err)
	}

	return npl, nil
}

// Delete all nodepool leases held by `leaseHolderID`
func (m *NodePoolLeaseManager) DeleteNodePoolLeases(leaseHolderID string) error {
	if m.Client == nil {
		return fmt.Errorf("NodePoolLeaseManager has not been initialized. missing client")
	}
	if m.LeaseNamespace == "" {
		return fmt.Errorf("NodePoolLeaseManager has not been initialized. missing namespace")
	}

	leases, err := m.Client.Leases(m.LeaseNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get list of leases: %v", err)
	}
	leasesToClear := []string{}
	for _, lease := range leases.Items {
		if *lease.Spec.HolderIdentity == leaseHolderID && strings.HasPrefix(lease.Name, LeaseNamePrefix) {
			leasesToClear = append(leasesToClear, lease.Name)
		}
	}
	m.Logger.Debugf("leases to clear: %s", leasesToClear)
	for _, lease := range leasesToClear {
		err = m.Client.Leases(m.LeaseNamespace).Delete(context.TODO(), lease, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete lease: %s %v", lease, err)
		}
		m.Logger.Debugf("lease successfully cleared: %s", lease)
	}

	return nil
}

// GetExistingLeaseLockedNodePools returns nodepool(s) which have been lease locked by other k8 controllers
func (m *NodePoolLeaseManager) GetExistingLeaseLockedNodePools(leaseHolderID string) ([]string, error) {
	if m.LeaseNamespace == "" {
		return nil, fmt.Errorf("NodePoolLeaseManager has not been initialized. missing namespace")
	}

	leases, err := m.Client.Leases(m.LeaseNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get list of leases: %v", err)
	}
	var lockedNodePools []string
	for _, lease := range leases.Items {
		if *lease.Spec.HolderIdentity != "" && *lease.Spec.HolderIdentity != leaseHolderID && strings.HasPrefix(lease.Name, LeaseNamePrefix) {
			nodepool := strings.TrimPrefix(lease.Name, LeaseNamePrefix)
			// we only care about nodepools that have enabled for lease feature
			if slices.Contains(m.LeaseEnabledNodepools, nodepool) {
				lockedNodePools = append(lockedNodePools, nodepool)
				m.Logger.Debugf("nodepool: %s is locked by: %s", nodepool, *lease.Spec.HolderIdentity)
			}
		}
	}
	m.Logger.WithField("lockedNodePools", lockedNodePools).Debug("nodepools locked by other controllers")
	return lockedNodePools, nil
}
