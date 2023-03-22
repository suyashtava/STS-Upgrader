package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.soma.salesforce.com/ajna/stsupgrader/pkg/lease"
	"git.soma.salesforce.com/ajna/stsupgrader/pkg/utils"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/apps/v1"
	api_v1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

const (
	ControllerRevisionHash = "controller-revision-hash"
)

// StsController struct defines how a controller should encapsulate
// logging, client connectivity, informing (list and watching)
// queueing, and handling of resource changes
type StsController struct {
	namespace     string
	labelSelector string
	clientset     kubernetes.Interface
	queue         workqueue.RateLimitingInterface
	stsLister     appslisters.StatefulSetLister
	stsSynched    cache.InformerSynced
	podLister     corelisters.PodLister
	podSynched    cache.InformerSynced
	recorder      record.EventRecorder
	LeaseManager *lease.LeaseManager
}

func NewStsController(
	namespace string,
	kubeclientset kubernetes.Interface,
	ssInformer appsinformers.StatefulSetInformer,
	podInformer coreinformers.PodInformer,
	recorder record.EventRecorder,
	lm *lease.LeaseManager) *StsController {

	controller := &StsController{
		namespace:  namespace,
		clientset:  kubeclientset,
		queue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "sts"),
		stsLister:  ssInformer.Lister(),
		stsSynched: ssInformer.Informer().HasSynced,
		podLister:  podInformer.Lister(),
		podSynched: podInformer.Informer().HasSynced,
		recorder:   recorder,
		LeaseManager: lm,
	}

	log.Info("setting up handlers")
	ssInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueSts,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueSts(new)
		},
	})

	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handlePod,
		UpdateFunc: func(old, new interface{}) {
			controller.handlePod(new)
		},
	})

	return controller

}

// Run is the main path of execution for the controller loopPod is up
func (s *StsController) Run(threadiness int, stopCh <-chan struct{}) error {
	// handle a panic with logging and exiting
	defer utilruntime.HandleCrash()
	// ignore new items in the queue but when all goroutines
	// have completed existing items then shutdown
	defer s.queue.ShutDown()

	log.Info("StsController.Run: initiating")

	// do the initial synchronization (one time) to populate resources
	if err := s.WaitForCacheSynch(stopCh); err != nil {
		return err
	}
	log.Info("StsController.Run: cache sync complete")
	log.Info("StsController.Run: Starting workers")
	for i := 0; i < threadiness; i++ {
		go wait.Until(s.runWorker, time.Second, stopCh)
	}

	log.Info("StsController.Run:Started workers")

	<-stopCh
	log.Info("StsController.Run:Shutting down workers")

	return nil
}

func (s *StsController) WaitForCacheSynch(stopCh <-chan struct{}) error {
	// do the initial synchronization (one time) to populate resources
	if !cache.WaitForCacheSync(stopCh, s.stsSynched, s.podSynched) {
		return fmt.Errorf("Error syncing cache")
	}
	return nil
}

// runWorker executes the loop to process new items added to the queue
func (s *StsController) runWorker() {
	// invoke processNextItem to fetch and consume the next change
	// to a watched or listed resource
	for s.processNextItem() {
		log.Debug("runWorker: processing next item")
	}

	log.Info("runWorker: exiting....")
}

// processNextItem retrieves each queued statefulset and takes the
// necessary handler action based off of if the sts was
// created or deleted
func (s *StsController) processNextItem() bool {
	// fetch the next item (blocking) from the queue to process or
	// if a shutdown is requested then return out of this to stop
	// processing
	obj, quit := s.queue.Get()

	// stop the worker loop from running as this indicates we
	// have sent a shutdown message that the queue has indicated
	// from the Get method
	if quit {
		return false
	}

	defer s.queue.Done(obj)

	var key string
	var ok bool

	// assert the string out of the key (format `namespace/name`)
	if key, ok = obj.(string); !ok {
		s.queue.Forget(obj)
		utilruntime.HandleError(fmt.Errorf("expected string in queue got %#v", obj))
		return true
	}

	if err := s.syncStsHandler(key); err != nil {
		// Put the item back on the workqueue to handle any transient errors.
		s.queue.AddRateLimited(key)
		utilruntime.HandleError(fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error()))
		return true
	}

	// Finally, if no error occurs we Forget this item so it does not
	// get queued again until another change happens.
	s.queue.Forget(obj)
	log.Debugf("Successfully synced '%s'", key)

	return true
}

func (s *StsController) syncStsHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	sts, err := s.stsLister.StatefulSets(namespace).Get(name)
	if err != nil {
		// The statefulset resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("Statefulset '%s' in queue no longer exists", key))
			return nil
		}

		return err
	}

	batchSize, err := utils.GetUpdateBatchSize(sts)
	if err != nil {
		// If an error occurs during syncStsHandler, we'll requeue the item so we can
		// attempt processing again later. This could have been caused by a
		// temporary network failure, or any other transient reason.
		return err
	}

	upgradeDomainsSize, err := utils.GetUpgradeDomainsSize(sts)
	utils.STSInfof("", "", "Setting upgradeDomainSize: %d.", upgradeDomainsSize)
	if err != nil {
		// If an error occurs during syncStsHandler, we'll requeue the item so we can
		// attempt processing again later. This could have been caused by a
		// temporary network failure, or any other transient reason.
		return err
    }

	s.LeaseManager.SetUpLeaseNamespaceAndLeaseName(sts)

	selector := utils.GetSelectorLabel(sts)
	pods, err := s.podLister.List(selector)
	if err != nil {
		utils.STSErrorf(sts.GetName(), "", "RollOut: Failed to list pods with label %+vwith error %v",
			selector, err)
		return err
	}

    tgtPods, err := s.findTarget(sts, sts.Status.UpdateRevision, pods, batchSize, upgradeDomainsSize)
	if err != nil {
		return err
	}

	if tgtPods != nil {
		for _, pod := range tgtPods {
			// If we add pods to the termination list, we must acquire a lease in order to modify nodepool
			s.LeaseManager.AcquireSTSLease(sts)
			err = s.deletePod(sts, pod)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *StsController) deletePod(sts *v1.StatefulSet, pod *api_v1.Pod) error {
	utils.STSInfof(sts.GetName(), pod.GetName(), "PodDeletion: Deleting Pod %s", pod.GetName())
	delOpts := metav1.DeleteOptions{}
	eviction := &policyv1beta1.Eviction{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "policy/v1beta1",
			Kind:       "Eviction",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		DeleteOptions: &delOpts,
	}
	err := s.clientset.PolicyV1beta1().Evictions(eviction.Namespace).Evict(context.TODO(), eviction)
	if err != nil {
		s.recorder.Eventf(sts, api_v1.EventTypeWarning, "FailedDeletingPod",
			"StatefulSet %s/%s: Pod %s/%s delete failed",
			sts.Namespace, s.namespace, sts.Name, pod.GetName(), err)
		return err
	}
	s.recorder.Eventf(sts, api_v1.EventTypeNormal, "DeletedPod",
		"StatefulSet %s/%s: Pod %s was deleted for OnDelete update",
		sts.Namespace, sts.Name, pod.GetName())

	return nil
}

func (s *StsController) enqueueSts(obj interface{}) {
	var key, name, namespace string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}

	// Convert the namespace/name string into a distinct namespace and name
	if namespace, name, err = cache.SplitMetaNamespaceKey(key); err != nil {
		utilruntime.HandleError(err)
		return
	}

	// Get the ss resource with this namespace/name
	sts, err := s.stsLister.StatefulSets(namespace).Get(name)
	if err != nil {
		// The statefulset resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("Statefulset '%s' no longer exists", key))
			return
		}

		return
	}

	if !utils.IsManagedByUpdater(sts) {
		log.Debugf("enqueueSts: ignoring sts %s (not managed by updater)", name)
		return
	}

	stsName := sts.GetName()
	if stsName == "" {
		utilruntime.HandleError(fmt.Errorf("%s: statefulset name must be specified", key))
		return
	}

	log.Debugf("enqueueSts: Sts %s Current revision %s Updated Revision %s", stsName,
		sts.Status.CurrentRevision,
		sts.Status.UpdateRevision)

	s.queue.Add(key)
}

func (s *StsController) handlePod(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		log.Debugf("handlePod: Recovered deleted pod '%s' from tombstone", object.GetName())
	}
	log.Debugf("handlePod: Processing Pod %s", object.GetName())
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		// If this object is not owned by a StatefulSet, we should not do anything more
		// with it.
		if ownerRef.Kind != "StatefulSet" {
			log.Debugf("handlePod: Ignoring Pod %s (not statefulset)", object.GetName())
			return
		}

		sts, err := s.stsLister.StatefulSets(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			log.Debugf("handlePod: ignoring orphaned pod '%s' of statefulset '%s'", object.GetSelfLink(),
				ownerRef.Name)
			return
		}
		s.enqueueSts(sts)

		return
	}
}

func (s *StsController) findTarget(
	sts *v1.StatefulSet,
	updateRevision string,
	pods []*api_v1.Pod,
	batchSize int,
	upgradeDomainsSize int) ([]*api_v1.Pod, error) {

	stsName := sts.GetName()
    upgradeDomainEnabled := len(pods) > upgradeDomainsSize && upgradeDomainsSize != utils.DefaultUpgradeDomainsSize
	retPods := make([]*api_v1.Pod, 0, batchSize)
	firstMissingOrdinal := -1

	utils.SortPods(pods)
	for i, pod := range pods {
		if (i != utils.GetOrdinal(pod)) {
			utils.STSInfof("Missing Pod Ordinal", strconv.Itoa(i), "")
			firstMissingOrdinal = i
			break
		}
	}

	if s.LeaseManager.CheckForRelease(sts, pods, updateRevision) {
		return nil, nil
	}

	if upgradeDomainEnabled {
		utils.STSDebugf("Sorting by UpgradeDomains", "", "")
		return s.getUDPodTargets(sts, updateRevision, pods, upgradeDomainsSize, firstMissingOrdinal)
	}
	mpods := make([]*api_v1.Pod, 0, 5)
	newUnhealthyPods := 0
	for _, pod := range pods {
		podid := utils.GetOrdinal(pod)
		if (firstMissingOrdinal >= 0 && podid >= firstMissingOrdinal) {
			// We want a proper estimate of the state of all pods in the STS, so abort any pod terminations until
			// the Pod re-appears.
			podNames := strings.Join(utils.GetPodNames(pods), ", ")
			utils.STSInfof(stsName, "",
				"Missing pod ordinal %d in Pod list %s. Stopping Pod terminations until it's back.",
				podid, podNames)
			return nil, nil
		}
		utils.STSDebugf(stsName, pod.GetName(), "Current revision %s - new revision %s (batch %d - %d)",
			pod.Labels[ControllerRevisionHash], updateRevision, batchSize, len(retPods))
		if pod.Labels[ControllerRevisionHash] == updateRevision {
			utils.STSDebugf(stsName, pod.GetName(), "Pod already updated to new revision, skipping termination")
			if !utils.IsHealthy(pod) {
				newUnhealthyPods++
			}
			continue
		}

		if utils.IsTerminating(pod) {
			// A terminating Pod is already updating itself, skip it.
			utils.STSInfof(stsName, pod.GetName(), "Pod already terminating, skipping from termination")
			newUnhealthyPods++
			continue
		}

		if !utils.IsHealthy(pod) {
			// Not healthy, we want to delete such pods at a higher priority (even if it's master), so prepend it
			retPods = append([]*api_v1.Pod{pod}, retPods...)
			utils.STSInfof(stsName, pod.GetName(), "Prepending unhealthy Pod to termination list")
			continue
		}

		if utils.IsMasterPod(pod) {
			// Avoid any designated master pod unless its the last pod left to be upgraded
			mpods = append(mpods, pod)
			utils.STSInfof(stsName, pod.GetName(), "Ignoring healthy master pod %s while creating update batch",
				pod.GetName())
			continue
		}

		// Append a healthy Pod to the termination list, if the list is not full yet.
		if len(retPods) < batchSize {
			retPods = append(retPods, pod)
			utils.STSInfof(stsName, pod.GetName(), "Appending healthy Pod to termination list")
		}
	}

	// The batch size (say N) of pods that should be updated must be decremented with the number of new pods that are
	// unhealthy or terminating. This ensures that at any given time, the actions of stsupgrader are not resulting in
	// more than N pods that are unhealthy. We do not count the number of "old" pods that are unhealthy because we are
	// anyway going to target those unhealthy old pods first in this update process (and hence not increase the count
	// of unhealthy pods).
	deleteSize := batchSize - newUnhealthyPods
	if deleteSize <= 0 {
		utils.STSInfof(stsName, "",
			"Number of updated unhealthy pods %d more than batchSize %d. Pausing terminations",
			newUnhealthyPods, batchSize)
		return nil, nil
	}

	originalRetSize := len(retPods)
	if originalRetSize > deleteSize {
		// This can happen if we prepended some unhealthy pods. Just truncate the list, as the most desirable targets
		// are at the beginning of the list.
		excludePods := retPods[deleteSize:]
		excludedPodNames := strings.Join(utils.GetPodNames(excludePods), ", ")
		retPods = retPods[0:deleteSize]
		includedPodNames := strings.Join(utils.GetPodNames(retPods), ", ")
		utils.STSInfof(stsName, "",
			"Truncating termination list from %d to %d, excluding %s and returning %s",
			originalRetSize, deleteSize, excludedPodNames, includedPodNames)
	} else if originalRetSize == 0 && len(mpods) > 0 {
		// Only if there are no more Pods left to update, do we go after master Pods
		if len(mpods) > 1 {
			podNames := strings.Join(utils.GetPodNames(mpods), ", ")
			utils.STSInfof(stsName, "",
				"Not updating master Pods as multiple master pods detected: %s. Return empty termination list",
				podNames)
		} else {
			// Master pod is the only one left to be upgraded.
			utils.STSInfof(stsName,  mpods[0].GetName(),
				"The only remaining pod to update is master Pod %s, putting it in termination list",
				mpods[0].GetName())
			retPods = append(retPods, mpods[0])
		}
	}
	return retPods, nil
}

func (s *StsController) getUDPodTargets(
	sts *v1.StatefulSet,
	updateRevision string,
	pods []*api_v1.Pod,
	upgradeDomainsSize int,
	firstMissingOrdinal int) ([]*api_v1.Pod, error) {
	stsName := sts.GetName()
	udPods := make([]*api_v1.Pod, 0, upgradeDomainsSize)
	// Sort by UDs
	utils.SortByUpgradeDomains(pods)

	// Identify the UD that needs to be updated
	pickedUpgradeDomain := -1
	unHealthyUD := -1
	for _, pod := range pods {
		podid := utils.GetOrdinal(pod)
		utils.STSDebugf(stsName, "", "Currently checking pod ordinal %d.", podid)
		upgradeDomain := podid % upgradeDomainsSize
		// If a pod is unhealthy, don't skip it and append it, so we can attempt to delete it at a higher priority.
		// Then mark that UD as unhealthy so that it does not proceed to the next UD until the current UD is healthy.
		if unHealthyUD < upgradeDomain && unHealthyUD != -1 {
			utils.STSInfof("Unhealthy node in previous UD ", strconv.Itoa(unHealthyUD), "")
			continue
		}

		if !utils.IsHealthy(pod) {
			if unHealthyUD == -1 {
				unHealthyUD = podid % upgradeDomainsSize
				utils.STSInfof("Flag unHealthyUD as", strconv.Itoa(unHealthyUD), "")
			}
		}

		if (podid > firstMissingOrdinal && firstMissingOrdinal >= 0){
			// We want a proper estimate of the state of all pods in the STS, so abort any pod terminations until
			// the Pod re-appears.
			podNames := strings.Join(utils.GetPodNames(pods), ", ")
			utils.STSInfof(stsName, "",
				"Missing pod ordinal %d in Pod list %s. Stopping Pod terminations until it's back.",
				podid, podNames)
			return nil, nil
		}

		if pod.Labels[ControllerRevisionHash] == updateRevision || utils.IsTerminating(pod) {
			utils.STSDebugf(stsName, pod.GetName(),
				"Pod already updated to new revision or terminating, skipping termination")
			// Already upgraded, keep looking
			continue
		}

		if pickedUpgradeDomain == -1 {
			// Found UD that has not yet been updated
			pickedUpgradeDomain = upgradeDomain
			udPods = append(udPods, pod)
		} else if pickedUpgradeDomain == upgradeDomain {
			// We are still in the UD that was picked for upgrade
			udPods = append(udPods, pod)
		} else {
			// we have gone past the UD that needs to be upgraded. Stop searching for more pods to upgrade
			break
		}
	}

	if pickedUpgradeDomain == -1 {
		// No UD was found to upgrade
		return nil, nil
	}

	return udPods, nil
}
