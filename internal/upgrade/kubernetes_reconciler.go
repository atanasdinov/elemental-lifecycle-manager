/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package upgrade

import (
	"context"
	"fmt"
	"time"

	upgradecattlev1 "github.com/rancher/system-upgrade-controller/pkg/apis/upgrade.cattle.io/v1"
	lifecyclev1alpha1 "github.com/suse/elemental-lifecycle-manager/api/v1alpha1"
	"github.com/suse/elemental-lifecycle-manager/internal/plan"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type PackagedComponentChartSnapshot struct {
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	ChartURL         string `json:"chartURL"`
	ChartContentSHA  string `json:"chartContentSHA"`
	ChartVersion     string `json:"chartVersion"`
	ReleaseRevisions int    `json:"releaseRevisions"`
}

type PackagedComponentsSnapshot struct {
	Charts []*PackagedComponentChartSnapshot
}

type PackagedComponentsSnapshotter interface {
	// Create creates a new packaged components snapshot and populates it with the found packaged components, returns
	// error otherwise.
	Create(ctx context.Context, nn types.NamespacedName) error
	// Load loads an existing packaged components snapshot into a 'PackagedComponentsSnapshot', returns error otherwise.
	Load(ctx context.Context, nn types.NamespacedName) (*PackagedComponentsSnapshot, error)
	// DeleteAndWait schedules a delete for the snapshot and NamespacedName and waits for it to be deleted.
	DeleteAndWait(ctx context.Context, nn types.NamespacedName, interval time.Duration, timeout time.Duration) error
}

type PackagedComponentsVerifier interface {
	// VerifyAvailability retrieves the packaged components from the provided snapshot, compares them with the
	// currently running packaged components and waits for any changed components to become available.
	VerifyAvailability(ctx context.Context, snapshot *PackagedComponentsSnapshot) (*PhaseStatus, error)
}

// KubernetesReconciler reconciles Kubernetes upgrades via SUC Plans and verifies node state.
type KubernetesReconciler struct {
	client.Client
	sucReconciler        PlanReconciler
	componentSnapshotter PackagedComponentsSnapshotter
	componentVerifier    PackagedComponentsVerifier
}

func NewKubernetesReconciler(
	c client.Client,
	sucReconciler PlanReconciler,
	componentSnapshotter PackagedComponentsSnapshotter,
	componentVerifier PackagedComponentsVerifier) *KubernetesReconciler {
	return &KubernetesReconciler{
		Client:               c,
		sucReconciler:        sucReconciler,
		componentSnapshotter: componentSnapshotter,
		componentVerifier:    componentVerifier,
	}
}

func (r *KubernetesReconciler) Phase() Phase {
	return PhaseKubernetes
}

func (r *KubernetesReconciler) ShouldReconcile(config *Config) bool {
	return config.Kubernetes != nil
}

func (r *KubernetesReconciler) Reconcile(ctx context.Context, config *Config) (*PhaseStatus, error) {
	logger := log.FromContext(ctx)
	k8sConfig := config.Kubernetes

	logger.Info("Reconciling Kubernetes upgrade",
		"image", k8sConfig.Image,
		"version", k8sConfig.Version,
		"release", config.ReleaseNamespacedName.Name)

	snapshotNN := types.NamespacedName{
		Name:      fmt.Sprintf("lcm-k8s-packaged-components-snapshot-pre-%s", lifecyclev1alpha1.SanitizeVersion(config.Version)),
		Namespace: config.ReleaseNamespacedName.Namespace,
	}

	// Snapshots are created one per version, so ignore "AlreadyExists" errors if the snapshot is already created
	if err := r.componentSnapshotter.Create(ctx, snapshotNN); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("creating Kubernetes packaged components snapshot: %w", err)
	}

	plans, err := r.preparePlans(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("preparing Kubernetes upgrade plans: %w", err)
	}

	for _, p := range plans {
		result, err := r.sucReconciler.Reconcile(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("reconciling Kubernetes upgrade plan '%s': %w", p.Name, err)
		}

		if result.Status.State != lifecyclev1alpha1.PlanComplete {
			return result.Status, nil
		}

		if !allNodesAtKubernetesVersion(result.Nodes, k8sConfig.Version) {
			return &PhaseStatus{
				State:   lifecyclev1alpha1.UpgradeInProgress,
				Message: fmt.Sprintf("Plan %s completed, waiting for node upgrade verification", p.Name),
			}, nil
		}
	}

	loadedSnapshot, err := r.componentSnapshotter.Load(ctx, snapshotNN)
	if err != nil {
		return nil, fmt.Errorf("loading Kubernetes packaged components snapshot: %w", err)
	}

	if status, err := r.componentVerifier.VerifyAvailability(ctx, loadedSnapshot); err != nil {
		return nil, fmt.Errorf("ensuring Kubernetes packaged components availability: %w", err)
	} else if status.State != lifecyclev1alpha1.K8sPackagedComponentsAvailable {
		return status, nil
	}

	if err := r.componentSnapshotter.DeleteAndWait(ctx, snapshotNN, 1*time.Second, 120*time.Second); err != nil {
		logger.Error(err, "cleaning up Kubernetes packaged components snapshot",
			"name", snapshotNN.Name,
			"namespace", snapshotNN.Namespace)
	}

	return &PhaseStatus{
		State:   lifecyclev1alpha1.UpgradeSucceeded,
		Message: "All nodes upgraded successfully",
	}, nil
}

func (r *KubernetesReconciler) preparePlans(ctx context.Context, config *Config) (plans []*upgradecattlev1.Plan, err error) {
	k8sConfig := config.Kubernetes
	cpPlan := plan.KubernetesControlPlane(config.ReleaseNamespacedName.Name, config.Version, k8sConfig.Version, k8sConfig.DrainOpts.ControlPlane)
	planList := []*upgradecattlev1.Plan{cpPlan}

	allNodes := &corev1.NodeList{}
	if err := r.List(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("listing cluster nodes: %w", err)
	}

	if !isControlPlaneOnlyCluster(allNodes.Items) {
		wkPlan := plan.KubernetesWorker(config.ReleaseNamespacedName.Name, config.Version, k8sConfig.Version, k8sConfig.DrainOpts.Worker)
		planList = append(planList, wkPlan)
	}

	return planList, nil
}
