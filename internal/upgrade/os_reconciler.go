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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	upgradecattlev1 "github.com/rancher/system-upgrade-controller/pkg/apis/upgrade.cattle.io/v1"
	lifecyclev1alpha1 "github.com/suse/elemental-lifecycle-manager/api/v1alpha1"
	"github.com/suse/elemental-lifecycle-manager/internal/plan"
)

// OSReconciler reconciles OS upgrades via SUC Plans and verifies node state.
type OSReconciler struct {
	planHandler
}

func NewOSReconciler(c client.Client) *OSReconciler {
	return &OSReconciler{
		planHandler: planHandler{Client: c},
	}
}

func (r *OSReconciler) Phase() Phase {
	return PhaseOS
}

func (r *OSReconciler) ShouldReconcile(config *Config) bool {
	return config.OS != nil
}

func (r *OSReconciler) Reconcile(ctx context.Context, config *Config) (*PhaseStatus, error) {
	logger := log.FromContext(ctx)
	osConfig := config.OS
	logger.Info("Reconciling OS upgrade",
		"image", osConfig.Image,
		"version", osConfig.Version,
		"release", config.ReleaseName)

	plans, err := r.preparePlans(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("preparing OS upgrade plans: %w", err)
	}

	for _, p := range plans {
		logger.Info("Reconciling OS upgrade plan",
			"plan", p.Name,
			"namespace", p.Namespace,
		)
		status, err := r.reconcilePlan(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("reconciling plan %s: %w", p.Name, err)
		}

		if status.State != lifecyclev1alpha1.PlanComplete {
			return status, nil
		}

		planNodes, err := r.listNodesForPlan(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("listing nodes for plan %s: %w", p.Name, err)
		}

		if !allNodesReady(planNodes.Items) {
			return &PhaseStatus{
				State:   lifecyclev1alpha1.UpgradeInProgress,
				Message: fmt.Sprintf("Plan %s completed, waiting for node upgrade verification", p.Name),
			}, nil
		}

		nodeNames := make([]string, 0, len(planNodes.Items))
		for _, n := range planNodes.Items {
			nodeNames = append(nodeNames, n.Name)
		}

		logger.Info("OS upgrade plan completed",
			"plan", p.Name,
			"namespace", p.Namespace,
			"upgradedNodes", nodeNames,
		)
	}

	return &PhaseStatus{
		State:   lifecyclev1alpha1.UpgradeSucceeded,
		Message: "All nodes upgraded successfully",
	}, nil
}

func (r *OSReconciler) preparePlans(ctx context.Context, config *Config) (plans []*upgradecattlev1.Plan, err error) {
	osConfig := config.OS
	cpPlan := plan.OSControlPlane(config.ReleaseName, osConfig.Image, osConfig.Version, osConfig.DrainOpts.ControlPlane)
	planList := []*upgradecattlev1.Plan{cpPlan}

	allNodes := &corev1.NodeList{}
	if err := r.List(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("listing cluster nodes: %w", err)
	}

	if !isControlPlaneOnlyCluster(allNodes.Items) {
		wkPlan := plan.OSWorker(config.ReleaseName, osConfig.Image, osConfig.Version, osConfig.DrainOpts.Worker)
		planList = append(planList, wkPlan)
	}

	return planList, nil
}
