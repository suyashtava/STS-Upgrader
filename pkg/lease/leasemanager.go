package lease

import (
	"context"
	"fmt"
	"git.soma.salesforce.com/ajna/stsupgrader/pkg/utils"
	v1 "k8s.io/api/apps/v1"
	api_v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/strings/slices"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"

	"git.soma.salesforce.com/kubernetes/kube-node-recycler/nodepoollease"
)

const (
	LeaseHolderID               = "stsupgrader"
)

type LeaseManager struct {
	LeaseList             map[string]*nodepoollease.NodePoolLease
	Context               context.Context
	Nplm                  nodepoollease.NodePoolLeaseManager
	Enabled               bool
}

func NewLeaseManager(logger logrus.FieldLogger, client coordinationv1client.LeasesGetter, leaseNameList []string, ctx context.Context, enableLease bool) *LeaseManager {
	nplm := *nodepoollease.NewNodePoolLeaseManager(logger, client, "", leaseNameList)
	return &LeaseManager{
		LeaseList:       make(map[string]*nodepoollease.NodePoolLease),
		Context:         ctx,
		Nplm:            nplm,
		Enabled:         enableLease,
	}
}

func (m *LeaseManager) DeleteLease(leaseHolderID string, ctx context.Context) error {
	if m.Nplm.Client == nil {
		return fmt.Errorf("LeaseManager has not been initialized. missing client")
	}
	if m.Nplm.LeaseNamespace == "" {
		return fmt.Errorf("LeaseManager has not been initialized. missing namespace")
	}
	leases, err := m.Nplm.Client.Leases(m.Nplm.LeaseNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get list of leases: %v", err)
	}
	if leases.Size() == 0 {
		m.Nplm.Logger.Infof("No leases to delete for leaseHolderId: " + leaseHolderID)
		return nil
	}
	leasesToClear := []string{}
	for _, lease := range leases.Items {
		// If holderIdentity is empty string that mean it must have released first
		if *lease.Spec.HolderIdentity == leaseHolderID && strings.HasPrefix(lease.Name, nodepoollease.LeaseNamePrefix) {
			leasesToClear = append(leasesToClear, lease.Name)
			m.Nplm.Logger.Infof("found lease to delete: %s", lease.Name)
		}
	}
	m.Nplm.Logger.Infof("leases to clear: %s", leasesToClear)
	for _, lease := range leasesToClear {
		err = m.Nplm.Client.Leases(m.Nplm.LeaseNamespace).Delete(ctx, lease, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete lease: %s %v", lease, err)
		}
		m.Nplm.Logger.Infof("lease successfully cleared: %s", lease)
	}

	return nil
}

func (m *LeaseManager) SetUpLeaseNamespaceAndLeaseName(sts *v1.StatefulSet) {
	if !m.Enabled {
		m.Nplm.Logger.Infof("Lease functionality is not enabled")
		return
	}
	leaseNamespace := utils.GetLeaseNamespace(sts)
	leaseName := utils.GetLeaseName(sts)
	m.Nplm.Logger.Infof("leaseNamespace: " + leaseNamespace + ", leaseName: " + leaseName)
	if  leaseNamespace == "" || leaseName == "" {
		return
	}
	// If an error occurs during syncStsHandler, we'll requeue the item so we can
	// attempt processing again later. This could have been caused by a
	// temporary network failure, or any other transient reason.
	utils.STSInfof("", "", "Setting Lease Namespace in Lease Manager: %s.", leaseNamespace)
	m.Nplm.LeaseNamespace = leaseNamespace

	if !slices.Contains(m.Nplm.LeaseEnabledNodepools, leaseName) {
		utils.STSInfof("", "", "Adding leaseName to leaseName list: %s.", leaseName)
		m.Nplm.LeaseEnabledNodepools = append(m.Nplm.LeaseEnabledNodepools, leaseName)
	}
}

func (m *LeaseManager) AcquireSTSLease(sts *v1.StatefulSet) {
	leaseName := utils.GetLeaseName(sts)
	if m.Nplm.LeaseNamespace == "" || !m.Enabled || leaseName == "" {
		return
	}
	// Get leaseName from statefulset annotation
	utils.STSInfof(sts.GetName(), "", "Attempting to acquire lease for statefulset: " + leaseName)
	if slices.Contains(m.Nplm.LeaseEnabledNodepools, leaseName) {
		var npl *nodepoollease.NodePoolLease
		var err error
		if _, ok := m.LeaseList[leaseName]; ok {
			// reuse lease
			npl = m.LeaseList[leaseName]
			m.Nplm.Logger.Debug("Found existing lease: " + leaseName)
		} else {
			// new lease
			npl, err = m.Nplm.CreateNodePoolLease(leaseName, LeaseHolderID, 48 * time.Hour, 47 * time.Hour, 60 * time.Second, 0)
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("failed to create lease: %w", err))
			} else {
				m.Nplm.Logger.Infof("Successfully created new lease: " + leaseName)
				// store
				m.LeaseList[leaseName] = npl
			}
		}

		// acquire
		if !npl.Acquire(m.Context) {
			utilruntime.HandleError(fmt.Errorf("Failed to acquire lease: " + leaseName))
		}
	}
}

func (m *LeaseManager) CheckForRelease(sts *v1.StatefulSet, pods []*api_v1.Pod, updateRevision string) bool {
	// Check if app still has a lease
	leaseName := utils.GetLeaseName(sts)
	if m.Nplm.LeaseNamespace == "" || !m.Enabled || leaseName == "" {
		return false
	}
	if _, ok := m.LeaseList[leaseName]; ok {
		m.Nplm.Logger.Info("About to enter final release with: " + leaseName)
		if m.finalReleaseAndDeleteLease(leaseName, pods, updateRevision, m.Context) {
			m.Nplm.Logger.Info("Clearing leaseNamespace: " + m.Nplm.LeaseNamespace)
			m.Nplm.LeaseNamespace = ""
			return true
		}
	}
	return false
}

func (m *LeaseManager) releaseSTSLease(leaseName string) bool {
	m.Nplm.Logger.Info("leaseName to release since done upgrading: " + leaseName)
	if _, ok := m.LeaseList[leaseName]; ok {
		// immediately release existing lock
		if !m.LeaseList[leaseName].Release() {
			utilruntime.HandleError(fmt.Errorf("failed to release lease"))
			return false
		}
		m.Nplm.Logger.Infof("released lease for %s", leaseName)
		// update
		delete(m.LeaseList, leaseName)
		return true
	}
	return false
}

// Deletes remaining lease object that is done upgrading
func (m *LeaseManager) finalReleaseAndDeleteLease(leaseName string, pods []*api_v1.Pod, updateRevision string, ctx context.Context) bool {
	// Check that all pods now match newest version and have completed upgrading
	if utils.CheckAllPodsDoneUpgrading(pods, updateRevision) {
		if m.releaseSTSLease(leaseName) {
			m.Nplm.Logger.Info("Attempting to clear lease for " + leaseName)
			// leaseHolderID is empty after successful release
			if err := m.DeleteLease("", ctx); err != nil {
				fmt.Errorf("failed to clear acquired lease: %v", err)
				return false
			} else {
				return true
			}
		}
	}
	return false
}
