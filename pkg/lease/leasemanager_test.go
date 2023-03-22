package lease

import (
	"context"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestLeaseManagerNewLeaseManager(t *testing.T) {
	client := fake.NewSimpleClientset()
	lm := NewLeaseManager(logrus.New(), client.CoordinationV1(), []string{"first", "second"}, context.TODO(), true)
	assert.NotNil(t, lm)
}

func TestLeaseManagerDeleteLeases(t *testing.T) {
	holderIdentity := "stsupgrader"
	lease := v1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "nodepool-first",
		},
		Spec: v1.LeaseSpec{
			HolderIdentity: &holderIdentity,
		},
	}
	leaseList := &v1.LeaseList{
		Items: []v1.Lease{lease},
	}
	kube := []runtime.Object{
		leaseList,
	}
	client := fake.NewSimpleClientset(kube...)
	nplm := NewLeaseManager(logrus.New(), client.CoordinationV1(), []string{"first", "second"}, context.TODO(), true)
	nplm.Nplm.LeaseNamespace = "default"

	err := nplm.DeleteLease("stsupgrader", context.TODO())
	assert.NoError(t, err)
}
