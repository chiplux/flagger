package canary

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flaggerv1 "github.com/weaveworks/flagger/pkg/apis/flagger/v1beta1"
)

func TestDeploymentController_Sync(t *testing.T) {
	mocks := newDeploymentFixture()
	err := mocks.controller.Initialize(mocks.canary, true)
	require.NoError(t, err)

	depPrimary, err := mocks.kubeClient.AppsV1().Deployments("default").Get("podinfo-primary", metav1.GetOptions{})
	require.NoError(t, err)

	dep := newDeploymentControllerTest()
	primaryImage := depPrimary.Spec.Template.Spec.Containers[0].Image
	sourceImage := dep.Spec.Template.Spec.Containers[0].Image
	assert.Equal(t, sourceImage, primaryImage)

	hpaPrimary, err := mocks.kubeClient.AutoscalingV2beta1().HorizontalPodAutoscalers("default").Get("podinfo-primary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, depPrimary.Name, hpaPrimary.Spec.ScaleTargetRef.Name)
}

func TestDeploymentController_Promote(t *testing.T) {
	mocks := newDeploymentFixture()
	err := mocks.controller.Initialize(mocks.canary, true)
	require.NoError(t, err)

	dep2 := newDeploymentControllerTestV2()
	_, err = mocks.kubeClient.AppsV1().Deployments("default").Update(dep2)
	require.NoError(t, err)

	config2 := newDeploymentControllerTestConfigMapV2()
	_, err = mocks.kubeClient.CoreV1().ConfigMaps("default").Update(config2)
	require.NoError(t, err)

	hpa, err := mocks.kubeClient.AutoscalingV2beta1().HorizontalPodAutoscalers("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	hpaClone := hpa.DeepCopy()
	hpaClone.Spec.MaxReplicas = 2

	_, err = mocks.kubeClient.AutoscalingV2beta1().HorizontalPodAutoscalers("default").Update(hpaClone)
	require.NoError(t, err)

	err = mocks.controller.Promote(mocks.canary)
	require.NoError(t, err)

	depPrimary, err := mocks.kubeClient.AppsV1().Deployments("default").Get("podinfo-primary", metav1.GetOptions{})
	require.NoError(t, err)

	primaryImage := depPrimary.Spec.Template.Spec.Containers[0].Image
	sourceImage := dep2.Spec.Template.Spec.Containers[0].Image
	assert.Equal(t, sourceImage, primaryImage)

	configPrimary, err := mocks.kubeClient.CoreV1().ConfigMaps("default").Get("podinfo-config-env-primary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, config2.Data["color"], configPrimary.Data["color"])

	hpaPrimary, err := mocks.kubeClient.AutoscalingV2beta1().HorizontalPodAutoscalers("default").Get("podinfo-primary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), hpaPrimary.Spec.MaxReplicas)
}

func TestDeploymentController_ScaleToZero(t *testing.T) {
	mocks := newDeploymentFixture()
	err := mocks.controller.Initialize(mocks.canary, true)
	require.NoError(t, err)

	err = mocks.controller.ScaleToZero(mocks.canary)
	require.NoError(t, err)

	c, err := mocks.kubeClient.AppsV1().Deployments("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(0), *c.Spec.Replicas)
}

func TestDeploymentController_NoConfigTracking(t *testing.T) {
	mocks := newDeploymentFixture()
	mocks.controller.configTracker = &NopTracker{}

	err := mocks.controller.Initialize(mocks.canary, true)
	require.NoError(t, err)

	depPrimary, err := mocks.kubeClient.AppsV1().Deployments("default").Get("podinfo-primary", metav1.GetOptions{})
	require.NoError(t, err)

	_, err = mocks.kubeClient.CoreV1().ConfigMaps("default").Get("podinfo-config-env-primary", metav1.GetOptions{})
	require.True(t, errors.IsNotFound(err), "Primary ConfigMap shouldn't have been created")

	configName := depPrimary.Spec.Template.Spec.Volumes[0].VolumeSource.ConfigMap.LocalObjectReference.Name
	assert.Equal(t, "podinfo-config-vol", configName)
}

func TestDeploymentController_HasTargetChanged(t *testing.T) {
	mocks := newDeploymentFixture()
	err := mocks.controller.Initialize(mocks.canary, true)
	require.NoError(t, err)

	// save last applied hash
	canary, err := mocks.flaggerClient.FlaggerV1beta1().Canaries("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	err = mocks.controller.SyncStatus(canary, flaggerv1.CanaryStatus{Phase: flaggerv1.CanaryPhaseInitializing})
	require.NoError(t, err)

	// save last promoted hash
	canary, err = mocks.flaggerClient.FlaggerV1beta1().Canaries("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	err = mocks.controller.SetStatusPhase(canary, flaggerv1.CanaryPhaseInitialized)
	require.NoError(t, err)

	dep, err := mocks.kubeClient.AppsV1().Deployments("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	depClone := dep.DeepCopy()
	depClone.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: *resource.NewQuantity(100, resource.DecimalExponent),
		},
	}

	// update pod spec
	_, err = mocks.kubeClient.AppsV1().Deployments("default").Update(depClone)
	require.NoError(t, err)

	canary, err = mocks.flaggerClient.FlaggerV1beta1().Canaries("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	// detect change in last applied spec
	isNew, err := mocks.controller.HasTargetChanged(canary)
	require.NoError(t, err)
	assert.True(t, isNew)

	// save hash
	err = mocks.controller.SyncStatus(canary, flaggerv1.CanaryStatus{Phase: flaggerv1.CanaryPhaseProgressing})
	require.NoError(t, err)

	dep, err = mocks.kubeClient.AppsV1().Deployments("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	depClone = dep.DeepCopy()
	depClone.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: *resource.NewQuantity(1000, resource.DecimalExponent),
		},
	}

	// update pod spec
	_, err = mocks.kubeClient.AppsV1().Deployments("default").Update(depClone)
	require.NoError(t, err)

	canary, err = mocks.flaggerClient.FlaggerV1beta1().Canaries("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	// ignore change as hash should be the same with last promoted
	isNew, err = mocks.controller.HasTargetChanged(canary)
	require.NoError(t, err)
	assert.False(t, isNew)

	depClone = dep.DeepCopy()
	depClone.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: *resource.NewQuantity(600, resource.DecimalExponent),
		},
	}

	// update pod spec
	_, err = mocks.kubeClient.AppsV1().Deployments("default").Update(depClone)
	require.NoError(t, err)

	canary, err = mocks.flaggerClient.FlaggerV1beta1().Canaries("default").Get("podinfo", metav1.GetOptions{})
	require.NoError(t, err)

	// detect change
	isNew, err = mocks.controller.HasTargetChanged(canary)
	require.NoError(t, err)
	assert.True(t, isNew)
}

func TestDeploymentController_Finalize(t *testing.T) {

	mocks := newDeploymentFixture()

	tables := []struct {
		mocks            deploymentControllerFixture
		callInitialize   bool
		shouldError      bool
		expectedReplicas int32
		canary           *flaggerv1.Canary
	}{
		//Primary not found returns error
		{mocks, false, false, 1, mocks.canary},
		//Happy path
		{mocks, true, false, 1, mocks.canary},
	}

	for _, table := range tables {
		if table.callInitialize {
			err := mocks.controller.Initialize(table.canary, true)
			if err != nil {
				t.Fatal(err.Error())
			}
		}

		err := mocks.controller.Finalize(table.canary)

		if table.shouldError && err == nil {
			t.Error("Expected error while calling Finalize, but none was returned")
		} else if !table.shouldError && err != nil {
			t.Errorf("Expected no error would be returned while calling Finalize, but returned %s", err)
		}

		if table.expectedReplicas > 0 {
			c, err := mocks.kubeClient.AppsV1().Deployments(mocks.canary.Namespace).Get(mocks.canary.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err.Error())
			}
			if int32Default(c.Spec.Replicas) != table.expectedReplicas {
				t.Errorf("Expected replicas %d recieved replicas %d", table.expectedReplicas, c.Spec.Replicas)
			}
		}
	}
}
