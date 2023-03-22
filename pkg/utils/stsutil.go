package utils

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	ManagedByUpdaterAnnotation = "stsupdater.salesforce.com/managed"
	BatchSizeUpdaterAnnotation = "stsupdater.salesforce.com/batch"
	UpgradeDomainsSizeUpdaterAnnotation = "stsupdater.salesforce.com/upgradedomains"
	MasterPodLabel             = "stsupdater.salesforce.com/masterpod"
	LeaseNameAnnotation = "stsupdater.salesforce.com/leasename"
	LeaseNamespaceAnnotation = "stsupdater.salesforce.com/leasenamespace"
	MasterPodValue             = "true"
	DefaultBatchSize           = 1
	DefaultUpgradeDomainsSize  = 0
	STSLogKey                  = "sts"
	PodLogKey                  = "stspod"
	ControllerRevisionHash = "controller-revision-hash"
)

var statefulPodRegex = regexp.MustCompile("(.*)-([0-9]+)$")

// GetParentName extracts sts name and ordinal number from pod definition.
func GetParentNameAndOrdinal(pod *v1.Pod) (string, int) {
	parent := ""
	ordinal := -1
	subMatches := statefulPodRegex.FindStringSubmatch(pod.Name)
	if len(subMatches) < 3 {
		return parent, ordinal
	}
	parent = subMatches[1]
	if i, err := strconv.ParseInt(subMatches[2], 10, 32); err == nil {
		ordinal = int(i)
	}
	return parent, ordinal
}

// GetParentName gets the name of pod's parent StatefulSet. If pod has not parent, the empty string is returned.
func GetParentName(pod *v1.Pod) string {
	parent, _ := GetParentNameAndOrdinal(pod)
	return parent
}

// GetOrdinal extracts the ordinal number of statefulset pod from its definition.
func GetOrdinal(pod *v1.Pod) int {
	_, ordinal := GetParentNameAndOrdinal(pod)
	return ordinal
}

// IsMemberOf tests if pod is a member of set.
func IsMemberOf(sts *apps.StatefulSet, pod *v1.Pod) bool {
	return GetParentName(pod) == sts.Name
}

// GetPodNames returns list of pod names from the list of pod objects
func GetPodNames(pods []*v1.Pod) []string {
	retNames := make([]string, len(pods))
	for i, pod := range pods {
		retNames[i] = pod.GetName()
	}

	return retNames
}

// IsManagedByUpdater checks if the sts should have its rolling upgrade managed by stsupgrader.
func IsManagedByUpdater(sts *apps.StatefulSet) bool {
	val, ok := sts.Annotations[ManagedByUpdaterAnnotation]
	if !ok {
		return false
	}

	if strings.ToLower(val) == "true" {
		return true
	}

	return false
}

// GetPodCondition extracts batch update size from sts's annotations.
// the batch size represents the number of pods to be deleted in each iteration.
func GetUpdateBatchSize(sts *apps.StatefulSet) (int, error) {
	val, ok := sts.Annotations[BatchSizeUpdaterAnnotation]
	if !ok {
		return DefaultBatchSize, nil
	}

	numval, err := strconv.Atoi(val)
	if err != nil {
		return 0, err
	}

	return numval, err
}

func GetUpgradeDomainsSize(sts *apps.StatefulSet) (int, error) {
	val, ok := sts.Annotations[UpgradeDomainsSizeUpdaterAnnotation]
	STSInfof("Get Upgrade Domain Annotation: ", "", val)
	if !ok {
		return DefaultUpgradeDomainsSize, nil
	}

	numval, err := strconv.Atoi(val)
	if err != nil {
		return 0, err
	}

	return numval, err
}

func GetLeaseName(sts *apps.StatefulSet) string {
	val, ok := sts.Annotations[LeaseNameAnnotation]
	STSInfof("Get Lease Name Annotation: ", "", val)
	if !ok {
		return ""
	}

	return val
}

func GetLeaseNamespace(sts *apps.StatefulSet) string {
	val, ok := sts.Annotations[LeaseNamespaceAnnotation]
	STSInfof("Get Lease Namespace Annotation: ", "", val)
	if !ok {
		return ""
	}

	return val
}

// Checks that all pods in a given nodepool from a specific app are done upgrading to newest version
func CheckAllPodsDoneUpgrading(pods []*v1.Pod, updateRevision string) bool {
	for _, pod := range pods {
		if pod.Labels[ControllerRevisionHash] != updateRevision || !IsHealthy(pod) {
			STSDebugf("", pod.GetName(), "Pods are not done upgrading or not entirely ready, " +
				"Current revision %s - new revision %s",
				pod.Labels[ControllerRevisionHash], updateRevision)
			return false
		}
	}
	STSDebugf("", "", "Pods are done upgrading to new revision %s", updateRevision)
	return true
}

func GetSelectorLabel(sts *apps.StatefulSet) labels.Selector {
	return labels.SelectorFromSet(labels.Set(sts.Spec.Selector.MatchLabels))
}

// GetPodCondition extracts the provided condition from the given status and returns that.
// Returns nil and -1 if the condition is not present, and the index of the located condition.
func GetPodCondition(status *v1.PodStatus, conditionType v1.PodConditionType) (int, *v1.PodCondition) {
	if status == nil {
		return -1, nil
	}
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return i, &status.Conditions[i]
		}
	}
	return -1, nil
}

// IsPodReady returns true if a pod is ready; false otherwise.
func IsPodReady(pod *v1.Pod) bool {
	_, condition := GetPodCondition(&pod.Status, v1.PodReady)
	return condition != nil && condition.Status == v1.ConditionTrue
}

// IsRunningAndReady returns true if pod is in the PodRunning Phase, if it has a condition of PodReady.
func IsRunningAndReady(pod *v1.Pod) bool {
	return pod.Status.Phase == v1.PodRunning && IsPodReady(pod)
}

// IsCreated returns true if pod has been created and is maintained by the API server
func IsCreated(pod *v1.Pod) bool {
	return pod.Status.Phase != ""
}

// IsFailed returns true if pod has a Phase of PodFailed
func IsFailed(pod *v1.Pod) bool {
	return pod.Status.Phase == v1.PodFailed
}

// IsTerminating returns true if pod's DeletionTimestamp has been set
func IsTerminating(pod *v1.Pod) bool {
	return pod.DeletionTimestamp != nil
}

// IsHealthy returns true if pod is running and ready and has not been terminated
func IsHealthy(pod *v1.Pod) bool {
	return IsRunningAndReady(pod) && !IsTerminating(pod)
}

func IsMasterPod(pod *v1.Pod) bool {
	if val, ok := pod.Labels[MasterPodLabel]; ok {
		if strings.ToLower(val) == MasterPodValue {
			return true
		}
	}
	return false
}

type AscendingOrdinal []*v1.Pod

func (ao AscendingOrdinal) Len() int {
	return len(ao)
}

func (ao AscendingOrdinal) Swap(i, j int) {
	ao[i], ao[j] = ao[j], ao[i]
}

func (ao AscendingOrdinal) Less(i, j int) bool {
	return GetOrdinal(ao[i]) < GetOrdinal(ao[j])
}

func SortPods(pods []*v1.Pod) {
	sort.Sort(AscendingOrdinal(pods))
}

type AscendingUpgradeDomain []*v1.Pod

func (ao AscendingUpgradeDomain) Len() int {
	return len(ao)
}

func (ao AscendingUpgradeDomain) Swap(i, j int) {
	ao[i], ao[j] = ao[j], ao[i]
}

func (ao AscendingUpgradeDomain) Less(i, j int) bool {
	val, ok := ao[i].Annotations[UpgradeDomainsSizeUpdaterAnnotation]
	STSInfof("", "", "Val: %s, Ok: %t.", val, ok)
	if ok {
		// We have UD annotation
		numval, err := strconv.Atoi(val)
		if err == nil {
			first := GetOrdinal(ao[i]) % numval
			second := GetOrdinal(ao[j]) % numval
			if first != second {
				// They are different UDs
				STSDebugf("", "", "Different UD so first: %d < second %d.", first, second)
				return first < second
			} else {
				// They are in same UD, make a more fine grained distinction by ordinal number
				STSDebugf("", "", "Same UD by ordinal number: i=%d < j=%d.", GetOrdinal(ao[i]),
					GetOrdinal(ao[j]))
				return GetOrdinal(ao[i]) < GetOrdinal(ao[j])
			}
		} else {
			// Don't make the STS upgrader crash just because a STS is misconfigured
			log.Error(err)
		}
	}
	return GetOrdinal(ao[i]) < GetOrdinal(ao[j])
}

func SortByUpgradeDomains(pods []*v1.Pod) {
	sort.Stable(AscendingUpgradeDomain(pods))
}

func STSDebugf(stsname string, pod string, format string, args ...interface{}) {
	log.WithFields(log.Fields{
		STSLogKey: stsname,
		PodLogKey: pod,
	}).Debugf(format, args...)
}

func STSInfof(stsname string, pod string, format string, args ...interface{}) {
	log.WithFields(log.Fields{
		STSLogKey: stsname,
		PodLogKey: pod,
	}).Infof(format, args...)
}

func STSErrorf(stsname string, pod string, format string, args ...interface{}) {
	log.WithFields(log.Fields{
		STSLogKey: stsname,
		PodLogKey: pod,
	}).Errorf(format, args...)
}

func STSWarnf(stsname string, pod string, format string, args ...interface{}) {
	log.WithFields(log.Fields{
		STSLogKey: stsname,
		PodLogKey: pod,
	}).Warnf(format, args...)
}