package utils

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"

	log "github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	VolumeIdentifier = "vol"
	PVCIdentifier    = "pvc"
)

// FakeAppLabel creates a label for the sts pods.
func FakeAppLabel() map[string]string {
	return map[string]string{"fapp": "fakehttpapp"}
}

// FakeAppAnnotation creates annotations used by stsupgrader
func FakeAppAnnotation(upgradeable bool, batchSize int64) map[string]string {
	return map[string]string{ManagedByUpdaterAnnotation: strconv.FormatBool(upgradeable),
		BatchSizeUpdaterAnnotation: strconv.FormatInt(batchSize, 10)}
}

// FakeAppAnnotationWithUpgradeDomain creates annotations including upgrade domains used by stsupgrader
func FakeAppAnnotationWithUpgradeDomain(upgradeable bool, batchSize int64, upgradeDomainsSize int64) map[string]string {
	return map[string]string{ManagedByUpdaterAnnotation: strconv.FormatBool(upgradeable),
		BatchSizeUpdaterAnnotation: strconv.FormatInt(batchSize, 10),
		UpgradeDomainsSizeUpdaterAnnotation: strconv.FormatInt(upgradeDomainsSize, 10)}
}

// SetStsVersion sets current and updated sts status versions.
func SetStsVersion(sts *appsv1.StatefulSet, curr int64, upd int64) {
	sts.Status.CurrentRevision = GetStrVersion(curr)
	sts.Status.UpdateRevision = GetStrVersion(upd)
}

// NewPVC creates PVC definition
func NewPVC(name string) v1.PersistentVolumeClaim {
	return v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.PersistentVolumeClaimSpec{
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceStorage: *resource.NewQuantity(1, resource.BinarySI),
				},
			},
		},
	}
}

// NewStatefulSetWithVolumes Create a fake statefulset with volumes
func NewStatefulSetWithVolumes(replicas int, namespace string, name string, petMounts []v1.VolumeMount,
	podMounts []v1.VolumeMount, updateVersion int64) *appsv1.StatefulSet {

	mounts := append(petMounts, podMounts...)
	claims := []v1.PersistentVolumeClaim{}
	for _, m := range petMounts {
		claims = append(claims, NewPVC(m.Name))
	}

	vols := []v1.Volume{}
	for _, m := range podMounts {
		vols = append(vols, v1.Volume{
			Name: m.Name,
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: fmt.Sprintf("/tmp/%v", m.Name),
				},
			},
		})
	}

	template := v1.PodTemplateSpec{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:         "nginx",
					Image:        "nginx",
					VolumeMounts: mounts,
				},
			},
			Volumes: vols,
		},
	}

	template.Labels = FakeAppLabel()

	sts := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("test"),
			Labels:    FakeAppLabel(),
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: FakeAppLabel(),
			},
			Replicas:             func() *int32 { i := int32(replicas); return &i }(),
			Template:             template,
			VolumeClaimTemplates: claims,
			ServiceName:          "fakesvc",
			UpdateStrategy:       appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			RevisionHistoryLimit: func() *int32 {
				limit := int32(2)
				return &limit
			}(),
		},
	}

	SetStsVersion(sts, updateVersion, updateVersion)

	return sts
}

// NewStatefulSet creates a fake statefulset
func NewStatefulSet(namespace string, name string, replicas int, version int64) *appsv1.StatefulSet {
	ssMounts := []v1.VolumeMount{
		{Name: "datadir", MountPath: "/tmp/zookeeper"},
	}
	podMounts := []v1.VolumeMount{
		{Name: "home", MountPath: "/home"},
	}
	return NewStatefulSetWithVolumes(replicas, namespace, name, ssMounts, podMounts, version)
}

// NewStatefulPod creates a pod definitions for the specified statefulset with specified volumes
func NewStatefulPod(ss *appsv1.StatefulSet, ordinalNum int, numVols int, numSourced int, isMaster bool,
	podVersion int64) *v1.Pod {

	ssKind := appsv1.SchemeGroupVersion.WithKind(reflect.TypeOf(appsv1.StatefulSet{}).Name())
	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            fmt.Sprintf("%s-%d", ss.Name, ordinalNum),
			Labels:          labels.Merge(nil, FakeAppLabel()),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(ss, ssKind)},
			Namespace:       ss.Namespace,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       reflect.TypeOf(v1.Pod{}).Name(),
			APIVersion: v1.SchemeGroupVersion.Version,
		},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			Conditions: []v1.PodCondition{{Type: v1.PodReady,
				Status: v1.ConditionTrue}},
		},
	}

	if isMaster {
		pod.Labels["stsupdater.salesforce.com/masterpod"] = "true"
	}

	if podVersion >= 0 {
		pod.Labels["controller-revision-hash"] = GetStrVersion(podVersion)
	}

	vols := make([]v1.Volume, numVols)
	for i := 0; i < numVols; i++ {
		suffix := fmt.Sprintf("%s-%d", pod.Name, i)
		v := v1.Volume{
			Name: fmt.Sprintf("%s-%s", VolumeIdentifier, suffix),
		}
		if i < numSourced {
			v.VolumeSource = v1.VolumeSource{
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("%s-%s", PVCIdentifier, suffix),
				},
			}
		}
		vols[i] = v
	}

	pod.Spec = v1.PodSpec{
		Volumes: vols,
	}
	return &pod
}

// CreateFakePod creates a pod definition for the specified statefulset
// if client is not nil, then a mock pod is created too.
func CreateFakePod(sts *appsv1.StatefulSet, client kubernetes.Interface, ordinalNum int, isMaster bool,
	podVersion int64) (*v1.Pod,
	error) {
	pod := NewStatefulPod(sts, ordinalNum, 0, 0, isMaster, podVersion)
	if client != nil {
		var err error

		log.Debugf("CreateFakePod: Creating Pod %s with fake API", pod.Name)
		pod, err = client.CoreV1().Pods(sts.Namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}
	return pod, nil
}

// CreateSSetAndPods creates statefulset and all its fake pods.
func CreateSSetAndPods(client kubernetes.Interface, namespace string, name string,
	replicas int, versionList []int64, stsVersion int64, upgradeDomainsSize int64) (*appsv1.StatefulSet, []*v1.Pod,
	error) {
	sts := NewStatefulSet(namespace, name, replicas, stsVersion)

	pods := make([]*v1.Pod, replicas)
	for i := 0; i < replicas; i++ {
		pod, err := CreateFakePod(sts, client, i, false, versionList[i])
		if err != nil {
			return nil, nil, err
		}
		pod.Annotations = map[string]string{
			UpgradeDomainsSizeUpdaterAnnotation: strconv.FormatInt(upgradeDomainsSize, 10)}
		pods[i] = pod
	}

	return sts, pods, nil
}

// CreateSSetAndMasterPods creates statefulset and all its fake pods.
func CreateSSetAndMasterPods(client kubernetes.Interface, namespace string, name string,
	replicas int, isMasterList []bool, versionList []int64, stsVersion int64, upgradeDomainsSize int64) (
	*appsv1.StatefulSet, []*v1.Pod, error) {
	sts := NewStatefulSet(namespace, name, replicas, stsVersion)

	pods := make([]*v1.Pod, replicas)
	for i := 0; i < replicas; i++ {
		pod, err := CreateFakePod(sts, client, i, isMasterList[i], versionList[i])
		if err != nil {
			return nil, nil, err
		}
		pod.Annotations = map[string]string{
			UpgradeDomainsSizeUpdaterAnnotation: strconv.FormatInt(upgradeDomainsSize, 10)}
		pods[i] = pod
	}

	return sts, pods, nil
}

// Convert numeric version to the string format expected in k8s
func GetStrVersion(version int64) string {
	return strconv.FormatInt(version, 10)
}

// Set the status of Pod to terminating
func SetPodToTerminating(pod *v1.Pod) {
	t := metav1.Now()
	pod.DeletionTimestamp = &t
}

// Clear the status of Pod termination
func ClearPodFromTerminating(pod *v1.Pod) {
	pod.DeletionTimestamp = nil
}

// Set pod status failed
func SetPodToFailed(pod *v1.Pod) {
	 pod.Status.Phase = v1.PodFailed
}

// Set pod status failed
func SetPodToRunning(pod *v1.Pod) {
	pod.Status.Phase = v1.PodRunning
}

func SetPodToPending(pod *v1.Pod) {
	pod.Status.Phase = v1.PodPending
}

// Set pod version
func SetPodVersion(pod *v1.Pod, podVersion int64) {
	pod.Labels["controller-revision-hash"] = GetStrVersion(podVersion)
}

// Create a fake recorder to validate steps.
type FakeRecorder struct {
	Events []string
}

func (f *FakeRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	f.Events = append(f.Events, fmt.Sprintf("%s %s %s", eventtype, reason, message))
}

func (f *FakeRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	f.Events = append(f.Events, fmt.Sprintf(eventtype+" "+reason+" "+messageFmt, args...))
}

func (f *FakeRecorder) PastEventf(object runtime.Object, timestamp metav1.Time, eventtype, reason, messageFmt string,
	args ...interface{}) {
}

func (f *FakeRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason,
	messageFmt string, args ...interface{}) {
	f.Eventf(object, eventtype, reason, messageFmt, args)
}

func (f *FakeRecorder) GetDeletedPods() []string {
	var ret []string
	for _, event := range f.Events {
		pat := regexp.MustCompile(": Pod (.*) was deleted for OnDelete update")
		s := pat.FindStringSubmatch(event)
		if len(s) == 2 {
			ret = append(ret, s[1])
		}
	}
	return ret
}
