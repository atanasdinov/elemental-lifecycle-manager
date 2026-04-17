/*
Copyright 2025-2026.

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

package v1alpha1

import (
	"context"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &Release{}).
		WithValidator(&ReleaseValidator{client: mgr.GetClient()}).
		Complete()
}

// Do not generate DeepCopy types for the validator as it is not an API data model
// +kubebuilder:object:generate=false
type ReleaseValidator struct {
	client client.Client
}

var _ admission.Validator[*Release] = &ReleaseValidator{}

func (r *ReleaseValidator) ValidateCreate(ctx context.Context, release *Release) (admission.Warnings, error) {
	if release.Spec.Registry == "" {
		return nil, fmt.Errorf("release registry is required")
	}

	if _, err := validateReleaseVersion(release.Spec.Version); err != nil {
		return nil, fmt.Errorf("validating release version: %w", err)
	}

	releaseList := &ReleaseList{}
	if err := r.client.List(ctx, releaseList); err != nil {
		return nil, fmt.Errorf("listing existing releases: %w", err)
	}

	if len(releaseList.Items) > 0 {
		return nil, fmt.Errorf(
			"cannot create release %s. The cluster has an already created Release object: %s/%s",
			release.Name,
			releaseList.Items[0].Namespace,
			releaseList.Items[0].Name,
		)
	}

	return nil, nil
}

func (r *ReleaseValidator) ValidateUpdate(_ context.Context, oldRelease, newRelease *Release) (admission.Warnings, error) {
	newReleaseVersion, err := validateReleaseVersion(newRelease.Spec.Version)
	if err != nil {
		return nil, err
	}

	if newRelease.Spec.Registry == "" {
		return nil, fmt.Errorf("release registry is required")
	}

	if err = validateNoUpgradeInProgress(oldRelease); err != nil {
		return nil, err
	}

	if oldRelease.Status.Version != "" {
		indicator, err := newReleaseVersion.Compare(oldRelease.Status.Version)
		if err != nil {
			return nil, fmt.Errorf("comparing versions: %w", err)
		}

		switch indicator {
		case 0:
			return nil, fmt.Errorf("any edits over '%s' must come with an increment of the version", newRelease.Name)
		case -1:
			return nil, fmt.Errorf("new version must be greater than the currently applied one ('%s')", oldRelease.Status.Version)
		}
	}

	return nil, nil
}

func (r *ReleaseValidator) ValidateDelete(_ context.Context, _ *Release) (admission.Warnings, error) {
	return nil, fmt.Errorf("deleting release objects is not allowed")
}

// validateNoUpgradeInProgress checks if an upgrade is currently in progress.
// Returns an error if the Applied condition is not True (upgrade in progress or failed).
func validateNoUpgradeInProgress(release *Release) error {
	appliedCond := apimeta.FindStatusCondition(release.Status.Conditions, ConditionApplied)

	switch {
	case appliedCond == nil:
		// No Applied condition means upgrade hasn't started yet, allow edits
		return nil
	case appliedCond.Status == metav1.ConditionTrue:
		// Previous upgrade completed successfully, allow edits
		return nil
	case appliedCond.Reason == UpgradeFailed:
		// Previous upgrade completed but failed, allow edits
		return nil
	}

	return fmt.Errorf("cannot edit while upgrade is in '%s' state", appliedCond.Reason)
}

func validateReleaseVersion(releaseVersion string) (*version.Version, error) {
	if releaseVersion == "" {
		return nil, fmt.Errorf("release version is required")
	}

	v, err := version.Parse(releaseVersion)
	if err != nil {
		return nil, fmt.Errorf("'%s' is not a semantic version", releaseVersion)
	}

	return v, nil
}
