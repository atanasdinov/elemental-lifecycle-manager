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

package reconcilers

import (
	"context"
	"fmt"
	"strings"

	upgradecattlev1 "github.com/rancher/system-upgrade-controller/pkg/apis/upgrade.cattle.io/v1"
	lifecyclev1alpha1 "github.com/suse/elemental-lifecycle-manager/api/v1alpha1"
	"github.com/suse/elemental-lifecycle-manager/internal/plan"
	"github.com/suse/elemental-lifecycle-manager/internal/upgrade"
	corev1 "k8s.io/api/core/v1"
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

type KubernetesPackagedComponentsHandler interface {
	// Snapshot creates a snapshot of the packaged components for a specific Kubernetes distribution.
	// Returns the packaged components snapshot, or an error otherwise.
	GenerateSnapshot(ctx context.Context, config *upgrade.Config) (*PackagedComponentsSnapshot, error)
	// ReconcileAvailability retrieves the packaged components from the provided snapshot, compares them with the
	// curently running packaged components and waits for any new or changed components to become available.
	ReconcileAvailability(ctx context.Context, snapshot *PackagedComponentsSnapshot) (*upgrade.PhaseStatus, error)
}

// KubernetesReconciler reconciles Kubernetes upgrades via SUC Plans and verifies node state.
type KubernetesReconciler struct {
	client.Client
	sucReconciler             PlanReconciler
	packagedComponentsHandler KubernetesPackagedComponentsHandler
}

func NewKubernetesReconciler(
	c client.Client,
	sucReconciler PlanReconciler,
	packagedComponentsHandler KubernetesPackagedComponentsHandler,
) *KubernetesReconciler {
	return &KubernetesReconciler{
		Client:                    c,
		sucReconciler:             sucReconciler,
		packagedComponentsHandler: packagedComponentsHandler,
	}
}

func (r *KubernetesReconciler) Phase() upgrade.Phase {
	return upgrade.PhaseKubernetes
}

func (r *KubernetesReconciler) Reconcile(ctx context.Context, config *upgrade.Config) (*upgrade.PhaseStatus, error) {
	if config == nil || config.Kubernetes == nil {
		return r.Phase().SkippedStatus(), nil
	}

	logger := log.FromContext(ctx)
	k8sConfig := config.Kubernetes
	logger.Info("Reconciling Kubernetes upgrade",
		"image", k8sConfig.Image,
		"version", k8sConfig.Version,
		"release", config.ReleaseNamespacedName.Name)

	snapshot, err := r.packagedComponentsHandler.GenerateSnapshot(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("generating Kubernetes packaged components snapshot: %w", err)
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
			return &upgrade.PhaseStatus{
				State:   lifecyclev1alpha1.UpgradeInProgress,
				Message: fmt.Sprintf("Plan %s completed, waiting for node upgrade verification", p.Name),
			}, nil
		}
	}

	if status, err := r.packagedComponentsHandler.ReconcileAvailability(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("ensuring Kubernetes packaged components availability: %w", err)
	} else if status.State != lifecyclev1alpha1.K8sPackagedComponentsAvailable {
		return status, nil
	}

	return &upgrade.PhaseStatus{
		State:   lifecyclev1alpha1.UpgradeSucceeded,
		Message: "All nodes upgraded successfully",
	}, nil
}

func (r *KubernetesReconciler) preparePlans(ctx context.Context, config *upgrade.Config) (plans []*upgradecattlev1.Plan, err error) {
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

// allNodesAtKubernetesVersion returns true if all nodes have the target Kubernetes version.
// Returns false if no nodes are provided.
// A node is considered upgraded when:
// - It is in Ready condition
// - It is not marked as unschedulable
// - Its kubelet version matches the target version
func allNodesAtKubernetesVersion(nodes []corev1.Node, targetVersion string) bool {
	if len(nodes) == 0 {
		return false
	}

	for _, node := range nodes {
		if !isNodeReady(&node) {
			return false
		}

		if node.Spec.Unschedulable {
			return false
		}

		if !kubeletVersionMatches(node.Status.NodeInfo.KubeletVersion, targetVersion) {
			return false
		}
	}

	return true
}

// isNodeReady returns true if the node has a Ready condition with status True.
func isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// kubeletVersionMatches checks if the kubelet version matches the target version.
// Handles version format differences (e.g., "v1.30.0" vs "1.30.0").
func kubeletVersionMatches(kubeletVersion, targetVersion string) bool {
	// Normalize both versions by removing 'v' prefix if present
	kubelet := strings.TrimPrefix(kubeletVersion, "v")
	target := strings.TrimPrefix(targetVersion, "v")

	return kubelet == target
}
