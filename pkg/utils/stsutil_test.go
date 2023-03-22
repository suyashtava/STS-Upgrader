package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
)

func TestUtils(t *testing.T) {
	sts := NewStatefulSet("testns", "nginx", 3, 1)
	pod, err := CreateFakePod(sts, nil, 99, false, 1)
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
	}

	otherSts := NewStatefulSet("testns", "apache", 3, 1)

	t.Run("Test GetParentNameAndOrdinal", func(t *testing.T) {
		name, num := GetParentNameAndOrdinal(pod)
		assert.Equal(t, "nginx", name)
		assert.Equal(t, 99, num)
	})

	t.Run("Test GetParentName", func(t *testing.T) {
		name := GetParentName(pod)
		assert.Equal(t, "nginx", name)
	})

	t.Run("Test GetOrdinal", func(t *testing.T) {
		num := GetOrdinal(pod)
		assert.Equal(t, 99, num)
	})

	t.Run("Test IsMemberOf", func(t *testing.T) {
		assert.False(t, IsMemberOf(otherSts, pod))
	})

	t.Run("Test IsManagedByUpdater", func(t *testing.T) {
		assert.False(t, IsManagedByUpdater(otherSts))
	})

	t.Run("Test GetUpdateBatchSize", func(t *testing.T) {
		num, err := GetUpdateBatchSize(otherSts)
		assert.NoError(t, err)
		// Since no annotation is explicitly set, it should default to 1.
		assert.Equal(t, 1, num)
	})

	// Apply annotations and try again.
	otherSts.Annotations = FakeAppAnnotation(true, 18)
	t.Run("Test IsManagedByUpdater", func(t *testing.T) {
		assert.True(t, IsManagedByUpdater(otherSts))
	})

	t.Run("Test GetUpdateBatchSize", func(t *testing.T) {
		num, err := GetUpdateBatchSize(otherSts)
		assert.NoError(t, err)
		// Since no annotation is explicitly set, it should default to 1.
		assert.Equal(t, 18, num)
	})

	t.Run("Test IsPodReady", func(t *testing.T) {
		assert.True(t, IsPodReady(pod))
	})

	t.Run("Test IsRunningAndReady", func(t *testing.T) {
		assert.True(t, IsRunningAndReady(pod))
	})

	t.Run("Test IsCreated", func(t *testing.T) {
		assert.True(t, IsCreated(pod))
	})

	t.Run("Test IsCreated", func(t *testing.T) {
		assert.True(t, IsCreated(pod))
	})

	t.Run("Test IsFailed", func(t *testing.T) {
		assert.False(t, IsFailed(pod))
	})

	t.Run("Test IsTerminating", func(t *testing.T) {
		assert.False(t, IsTerminating(pod))
	})

	t.Run("Test IsHealthy", func(t *testing.T) {
		assert.True(t, IsHealthy(pod))
	})

	t.Run("Test SortPods", func(t *testing.T) {
		var pods []*v1.Pod
		for i := 20; i > 15; i-- {
			pod, err := CreateFakePod(sts, nil, i, false, 1)
			if err != nil {
				t.Errorf("Unexpected error: %s", err)
			}
			pods = append(pods, pod)
		}

		expected := []string{"nginx-20", "nginx-19", "nginx-18", "nginx-17", "nginx-16"}
		for i, p := range pods {
			assert.Equal(t, expected[i], p.Name)
		}

		SortPods(pods)

		expected = []string{"nginx-16", "nginx-17", "nginx-18", "nginx-19", "nginx-20"}
		for i, p := range pods {
			assert.Equal(t, expected[i], p.Name)
		}
	})

	t.Run("Test GetPodNames", func(t *testing.T) {
		var pods []*v1.Pod
		for i := 0; i < 5; i++ {
			pod, err := CreateFakePod(sts, nil, i, false, 1)
			if err != nil {
				t.Errorf("Unexpected error: %s", err)
			}
			pods = append(pods, pod)
		}

		pod_names := GetPodNames(pods)
		expected := []string{"nginx-0", "nginx-1", "nginx-2", "nginx-3", "nginx-4"}
		for i, pname := range pod_names {
			assert.Equal(t, expected[i], pname)
		}
	})

}
