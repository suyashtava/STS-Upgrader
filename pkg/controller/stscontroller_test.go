package controller

import (
	"context"
	"git.soma.salesforce.com/ajna/stsupgrader/pkg/lease"
	"strings"
	"testing"
	"time"

	"git.soma.salesforce.com/ajna/stsupgrader/pkg/utils"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	api_v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	v12 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	TestNamespace = "testns"
	OldVersion    = 1
	NewVersion    = 2
)

type controllerMocks struct {
	ssInformer  appsinformers.StatefulSetInformer
	podInformer v12.PodInformer
	controller  *StsController
}

func createFakeController(namespace string, stopCh chan struct{}, threadiness int) controllerMocks {

	c := controllerMocks{}
	client := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactoryWithOptions(client, time.Minute*10,
		informers.WithNamespace(namespace))
	c.ssInformer = informerFactory.Apps().V1().StatefulSets()
	c.podInformer = informerFactory.Core().V1().Pods()

	var nplm = lease.NewLeaseManager(log.New(), client.CoordinationV1(), []string{"first", "second"}, context.TODO(), false)
	c.controller = NewStsController(namespace, client, c.ssInformer, c.podInformer, &utils.FakeRecorder{}, nplm)

	informerFactory.Start(stopCh)

	// Start control
	go c.controller.Run(threadiness, stopCh)

	return c
}

// Confirm when annotation is done, that upgrade happens
func TestBasicUpgradeBatching(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	// All pods are on an old version
	podVersionList := []int64{OldVersion, OldVersion, OldVersion}
	sts, _, err := utils.CreateSSetAndPods(c.controller.clientset, TestNamespace, "foo", 3,
		podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Set annotation to do the upgrade
	sts.Annotations = utils.FakeAppAnnotation(true, 2)

	deletedPods := runControllerSteps(t, c, stopCh, sts, true)
	if deletedPods == nil {
		t.Fatalf("Deleted pods returned nil")
	}
	// First two pods should have been deleted.
	assert.Equal(t, []string{"foo-0", "foo-1"}, deletedPods, "Value did not match %s", deletedPods)
}

// Confirm when annotation is done, that upgrade happens
func TestBasicUpgradeBatchingWithUpgradeDomains(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	// All pods are on an old version
	podVersionList := []int64{OldVersion, OldVersion, OldVersion, OldVersion, OldVersion}
	sts, pods, err := utils.CreateSSetAndPods(c.controller.clientset, TestNamespace, "foo", 5,
		podVersionList, NewVersion, 2)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Set annotation to do the upgrade
	sts.Annotations = utils.FakeAppAnnotationWithUpgradeDomain(true, 1, 2)

	expectedNames := []string{"foo-0", "foo-2", "foo-4"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames, 1, 2)

	close(stopCh)
}

// Confirm when annotation is not set, no upgrade is done.
func TestIgnoreSTSWithNoAnnotation(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	// All pods are on an old version
	podVersionList := []int64{OldVersion, OldVersion, OldVersion}
	sts, _, err := utils.CreateSSetAndPods(c.controller.clientset, TestNamespace, "foo", 3,
		podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}

	log.Infoln(sts)

	deletedPods := runControllerSteps(t, c, stopCh, sts, false)
	// No pods should have been deleted, since STS did not have annotation.
	assert.Equal(t, len(deletedPods), 0)
}

// Confirm that when some pods have already been upgraded, only the non-upgraded ones get deleted.
func TestUpgradeOnlyOldPods(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	// Only one pod is on old version
	podVersionList := []int64{NewVersion, OldVersion, NewVersion}
	sts, _, err := utils.CreateSSetAndPods(c.controller.clientset, TestNamespace, "foo", 3,
		podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Set annotation to do the upgrade
	sts.Annotations = utils.FakeAppAnnotation(true, 2)

	deletedPods := runControllerSteps(t, c, stopCh, sts, true)
	if deletedPods == nil {
		t.Fatalf("Deleted pods returned nil")
	}
	// Only the non-upgraded pod foo-1 should be upgraded.
	assert.Equal(t, []string{"foo-1"}, deletedPods, "Value did not match %s", deletedPods)
}

// Confirm that when some pods have already been upgraded, only the non-upgraded ones get deleted.
func TestUpgradeOnlyOldPodsWithUpgradeDomains(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	// Only one pod is on old version
	podVersionList := []int64{NewVersion, NewVersion, NewVersion, NewVersion, OldVersion}
	sts, _, err := utils.CreateSSetAndPods(c.controller.clientset, TestNamespace, "foo", 5,
		podVersionList, NewVersion, 2)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Set annotation to do the upgrade
	sts.Annotations = utils.FakeAppAnnotationWithUpgradeDomain(true, 1, 2)

	deletedPods := runControllerSteps(t, c, stopCh, sts, true)
	if deletedPods == nil {
		t.Fatalf("Deleted pods returned nil")
	}
	// Only the non-upgraded pod foo-4 should be upgraded.
	assert.Equal(t, []string{"foo-4"}, deletedPods, "Value did not match %s", deletedPods)
}

// Confirm that master pod is done last during upgrade
func TestSkipMaster(t *testing.T) {

	log.SetLevel(log.DebugLevel)
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	// designate foo-1 as master pod
	isMasterList := []bool{false, true, false}
	// All pods are on an old version
	podVersionList := []int64{OldVersion, OldVersion, OldVersion}
	sts, _, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 3,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Set annotation to do the upgrade
	sts.Annotations = utils.FakeAppAnnotation(true, 3)

	deletedPods := runControllerSteps(t, c, stopCh, sts, true)
	if deletedPods == nil {
		t.Fatalf("Deleted pods returned nil")
	}

	// Should have skipped the master pod foo-1 since we still have other pods that had to be updated.
	assert.Equal(t, []string{"foo-0", "foo-2"}, deletedPods, "Value did not match %s", deletedPods)
}

// Confirm that master pod is done last during upgrade
func TestUpdateMasterAtEnd(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)
	// designate foo-1 as master pod
	isMasterList := []bool{false, true, false}
	// make sure master version is the only one not updated yet.
	podVersionList := []int64{NewVersion, OldVersion, NewVersion}
	sts, _, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 3,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Set annotation to do the upgrade
	sts.Annotations = utils.FakeAppAnnotation(true, 4)

	deletedPods := runControllerSteps(t, c, stopCh, sts, true)
	if deletedPods == nil {
		t.Fatalf("Deleted pods returned nil")
	}
	// As only master pod foo-1 should be updated as all other Pods have already been updated.
	assert.Equal(t, []string{"foo-1"}, deletedPods, "Value did not match %s", deletedPods)
}

// Confirm that multiple master pods are not upgraded
func TestHandleMultiMaster(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)
	// designate foo-1 and foo-2 as master pods
	isMasterList := []bool{false, true, true}
	// make sure master version is the only one not updated yet.
	podVersionList := []int64{NewVersion, OldVersion, OldVersion}
	sts, _, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 3,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Set annotation to do the upgrade
	sts.Annotations = utils.FakeAppAnnotation(true, 4)

	deletedPods := runControllerSteps(t, c, stopCh, sts, true)
	// Since we do nothing if multiple master pods are found, no pod should be deleted.
	assert.Equal(t, len(deletedPods), 0)
}

// Common code used by tests to run controller through a STS processing step.
func runControllerSteps(t *testing.T, c controllerMocks, stopCh chan struct{},
	sts *v1.StatefulSet, isSTSQueued bool) []string {
	if err := c.controller.WaitForCacheSynch(stopCh); err != nil {
		t.Errorf("Unexpected error: %s", err)
		return nil
	}
	stsIndexer := c.ssInformer.Informer().GetIndexer()
	queueLen := 0
	if isSTSQueued {
		queueLen = 1
	}

	if err := stsIndexer.Add(sts); err != nil {
		t.Errorf("Unexpected error: %s", err)
		return nil
	}

	c.controller.enqueueSts(sts)
	assert.Equal(t, c.controller.queue.Len(), queueLen)

	// Only invoke processNextItem() if the sts actually got queued, otherwise the call hangs.
	if queueLen > 0 {
		c.controller.processNextItem()
	}

	// As only master pod foo-1 should be updated as all other Pods have already been updated.
	deletedPods := c.controller.recorder.(*utils.FakeRecorder).GetDeletedPods()

	close(stopCh)
	assert.False(t, c.controller.processNextItem())

	return deletedPods
}

func TestPodSelectionWithoutMaster(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	log.SetLevel(log.DebugLevel)
	isMasterList := []bool{
		false, false, false,
		false, false, false,
		false, false, false,
	}
	// make sure master version is the only one not updated yet.
	podVersionList := []int64{
		OldVersion, OldVersion, OldVersion,
		OldVersion, OldVersion, OldVersion,
		NewVersion, OldVersion, OldVersion,
	}

	// Just instantiate fake objects, no need to fake API (hence client is nil) for these unit tests.
	sts, pods, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 9,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Make sure it skips a terminating Pod for deletion
    utils.SetPodToTerminating(pods[5])
	// Check if first batch shows preference to failed Pods
	utils.SetPodToFailed(pods[7])
	utils.SetPodToFailed(pods[8])

	// Failed ones were prepended to the list as they were found.
	expectedNames := []string{"foo-8", "foo-7"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-0", "foo-1"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-2", "foo-3"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-4"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	// Finally no more targets
	c.controller.simulateUpgrade(t, sts, pods,  []string{} , 3, utils.DefaultUpgradeDomainsSize)

	close(stopCh)
}

func TestPodSelectionWithMaster(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	log.SetLevel(log.DebugLevel)
	isMasterList := []bool{
		true, false, false,
		false, false, false,
		false, false, false,
	}
	// make sure master version is the only one not updated yet.
	podVersionList := []int64{
		OldVersion, OldVersion, OldVersion,
		OldVersion, OldVersion, OldVersion,
		NewVersion, OldVersion, OldVersion,
	}

	// Just instantiate fake objects, no need to fake API (hence client is nil) for these unit tests.
	sts, pods, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 9,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Make sure it skips a terminating Pod for deletion
	utils.SetPodToTerminating(pods[5])
	// Check if first batch shows preference to failed Pods
	utils.SetPodToFailed(pods[7])
	utils.SetPodToFailed(pods[8])

	// Failed ones were prepended to the list as they were found, master (foo-0) is skipped.
	expectedNames := []string{"foo-8", "foo-7"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-1", "foo-2"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-3", "foo-4"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	// Finally, master is updated
	expectedNames = []string{"foo-0"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	// Finally no more targets
	c.controller.simulateUpgrade(t, sts, pods,  []string{} , 3, utils.DefaultUpgradeDomainsSize)

	close(stopCh)
}

func TestPodSelectionWithFailedMaster(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	log.SetLevel(log.InfoLevel)
	isMasterList := []bool{
		false, false, false,
		false, true, false,
		false, false, false,
	}
	// make sure master version is the only one not updated yet.
	podVersionList := []int64{
		OldVersion, OldVersion, OldVersion,
		OldVersion, OldVersion, OldVersion,
		NewVersion, OldVersion, OldVersion,
	}

	// Just instantiate fake objects, no need to fake API (hence client is nil) for these unit tests.
	sts, pods, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 9,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}
	// Make sure it skips a terminating Pod for deletion
	utils.SetPodToTerminating(pods[5])
	// Check if first batch shows preference to failed Pods, even when one of them is master
	utils.SetPodToFailed(pods[4])
	utils.SetPodToFailed(pods[8])

	// Failed ones were prepended to the list as they were found, including the failing master foo-4
	// update batch size is reduced to account for the terminating pod
	expectedNames := []string{"foo-8", "foo-4"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

    // Next, healthy pods are updated
	expectedNames = []string{ "foo-0", "foo-1"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-2", "foo-3"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-7"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	// Finally no more targets
	c.controller.simulateUpgrade(t, sts, pods,  []string{} , 3, utils.DefaultUpgradeDomainsSize)

	close(stopCh)
}

func TestPodSelectionWithMissingPod(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	log.SetLevel(log.InfoLevel)
	isMasterList := []bool{
		false, false, false,
		false, false, false,
	}
	// make sure master version is the only one not updated yet.
	podVersionList := []int64{
		OldVersion, OldVersion, OldVersion,
		OldVersion, OldVersion, NewVersion,
	}

	// Just instantiate fake objects, no need to fake API (hence client is nil) for these unit tests.
	sts, pods, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 6,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}

	// skip foo-2
	pods = append(pods[:2], pods[3:]...)
	// Make sure it skips a terminating Pod for deletion

	// A missing pod makes the upgrade process stop, until the pod re-appears
	expectedNames := []string{}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	close(stopCh)
}

func TestPodSelectionWithUnhealthyUpdatedPods(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	log.SetLevel(log.InfoLevel)
	isMasterList := []bool{
		false, false, false,
		false, false, false,
		false, false, false,
	}
	podVersionList := []int64{
		NewVersion, NewVersion, OldVersion,
		OldVersion, OldVersion, OldVersion,
		OldVersion, OldVersion, NewVersion,
	}

	// Just instantiate fake objects, no need to fake API (hence client is nil) for these unit tests.
	sts, pods, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 9,
		isMasterList, podVersionList, NewVersion, utils.DefaultUpgradeDomainsSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}

	// Set two new pods to unhealthy
	utils.SetPodToFailed(pods[0])
	utils.SetPodToFailed(pods[1])
	// Set one to terminating
	utils.SetPodToTerminating(pods[4])

	// Since 3 pods are terminating or unhealthy, update process should stall
	expectedNames := []string{}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	// Make the new pods healthy
	utils.SetPodToRunning(pods[0])
	utils.SetPodToRunning(pods[1])

	// Upgrades should now proceed
	expectedNames = []string{"foo-2", "foo-3"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	// Upgrades should now proceed
	expectedNames = []string{"foo-5", "foo-6"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	expectedNames = []string{"foo-7"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	// Upgrade is complete
	expectedNames = []string{}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 3, utils.DefaultUpgradeDomainsSize)

	close(stopCh)
}

func TestPodSelectionWithUpgradeDomainsAndUnhealthyUpdatedPods(t *testing.T) {
	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	// Start controller with no worker threads. We are going to single step through the processing.
	c := createFakeController(TestNamespace, stopCh, 0)

	log.SetLevel(log.InfoLevel)
	isMasterList := []bool{
		false, false, false,
		false, false, false,
		false, false, false,
	}
	// make sure an early upgrade domain is updated complete to simulate unhealthiness
	podVersionList := []int64{
		NewVersion, OldVersion, OldVersion,
		NewVersion, OldVersion, OldVersion,
		OldVersion, OldVersion, OldVersion,
	}

	// Just instantiate fake objects, no need to fake API (hence client is nil) for these unit tests.
	sts, pods, err := utils.CreateSSetAndMasterPods(c.controller.clientset, TestNamespace, "foo", 9,
		isMasterList, podVersionList, NewVersion, 3)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}

	sts.Annotations = utils.FakeAppAnnotationWithUpgradeDomain(true, 1, 3)
	// Pods initially sorted in order: 0 1 2 3 4 5 6 7 8 9
	// Set one new pod to unhealthy
	utils.SetPodToFailed(getCorrectLocationOfPod(pods, 3))
	// Set one new pod and one old pod to terminating
	utils.SetPodToTerminating(getCorrectLocationOfPod(pods, 6))
	utils.SetPodToTerminating(getCorrectLocationOfPod(pods, 1))

	// Since 3 pods are terminating or unhealthy, update process should stall
	expectedNames := []string{}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)
	// After this line the pods are now sorted according to UD: 0 3 6 | 1 4 7 | 2 5 8
	// Make the new pod healthy
	utils.SetPodToRunning(getCorrectLocationOfPod(pods, 3))

	expectedNames = []string{}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)

	utils.ClearPodFromTerminating(getCorrectLocationOfPod(pods, 6))

	expectedNames = []string{"foo-6"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)

	// Upgrades should now proceed
	expectedNames = []string{"foo-4", "foo-7"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)

	// Since there's still an unhealthy pod in a prior upgrade domain, we should still stall
	expectedNames = []string{}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)

	utils.ClearPodFromTerminating(getCorrectLocationOfPod(pods, 1))

	// Upgrades should now proceed
	expectedNames = []string{"foo-1"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)

	// Upgrades should now proceed
	expectedNames = []string{"foo-2", "foo-5", "foo-8"}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)

	// Upgrade is complete
	expectedNames = []string{}
	c.controller.simulateUpgrade(t, sts, pods,  expectedNames , 1, 3)

	close(stopCh)
}

// This method returns the correct pod by searching through their ordinal numbers
func getCorrectLocationOfPod(pods []*api_v1.Pod, targetPod int) *api_v1.Pod {
	for i := 0; i < len(pods); i++ {
		podid := utils.GetOrdinal(pods[i])
		if (podid == targetPod) {
			return pods[i]
		}
	}
	return nil
}

func (c *StsController) simulateUpgrade(t *testing.T, sts *v1.StatefulSet, pods []*api_v1.Pod, expectedNames []string, batchSize int,
	upgradeDomainSize int) {
	if upgradeDomainSize == utils.DefaultUpgradeDomainsSize {
		upgradeDomainSize = len(pods)
	} else {
		batchSize = len(pods)
	}
	tgtPods, err := c.findTarget(sts, utils.GetStrVersion(NewVersion), pods,
		batchSize, upgradeDomainSize)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
		return
	}

    if !assert.Equal(t,len(expectedNames), len(tgtPods)) {
		retStr := strings.Join(utils.GetPodNames(tgtPods), ", ")
		expectedStr := strings.Join(expectedNames, ",")
		t.Fatalf("Expected %s != %s", expectedStr, retStr)
	}

	for i, pod := range tgtPods {
		log.Debugf("Found Pod: %s", pod.Name)

		if !assert.Equal(t, expectedNames[i], pod.GetName()) {
			t.FailNow()
		}
		// Update version to simulate update process.
		utils.SetPodVersion(pod, NewVersion)
		// They come up healthy.
		utils.SetPodToRunning(pod)
	}

	log.Infof("=> Batch update done")
}
