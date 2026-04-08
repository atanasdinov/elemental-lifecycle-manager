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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	helmv1 "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	lifecyclev1alpha1 "github.com/suse/elemental-lifecycle-manager/api/v1alpha1"
	"github.com/suse/elemental-lifecycle-manager/internal/helm"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	chartURLAnnotation = "helm.cattle.io/chart-url"
)

type RKE2PackagedComponentsSnapshotter struct {
	client.Client
	helmClient     helm.Client
	findComponents func(ctx context.Context, client client.Client) (map[string]helmv1.HelmChart, error)
}

func NewRKE2PackagedComponentSnapshotter(
	c client.Client,
	helm helm.Client,
	findComponents func(ctx context.Context, client client.Client) (map[string]helmv1.HelmChart, error),
) *RKE2PackagedComponentsSnapshotter {
	snapshotter := &RKE2PackagedComponentsSnapshotter{
		Client:         c,
		helmClient:     helm,
		findComponents: findComponents,
	}

	if snapshotter.findComponents == nil {
		snapshotter.findComponents = findRKE2PackagedComponents
	}

	return snapshotter
}

func (r *RKE2PackagedComponentsSnapshotter) Create(ctx context.Context, nn types.NamespacedName) error {
	logger := log.FromContext(ctx)
	snapshot := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nn.Name,
			Namespace: nn.Namespace,
		},
	}

	if err := r.populateSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf("populating RKE2 packaged components snapshot: %w", err)
	}

	logger.Info("Creating RKE2 packaged components snapshot",
		"name", snapshot.Name,
		"namespace", snapshot.Namespace)
	return r.Client.Create(ctx, snapshot)
}

func (r *RKE2PackagedComponentsSnapshotter) populateSnapshot(ctx context.Context, snapshot *corev1.ConfigMap) error {
	rke2Charts, err := r.findComponents(ctx, r.Client)
	if err != nil {
		return fmt.Errorf("retrieving RKE2 packaged components: %w", err)
	}

	if snapshot.Data == nil {
		snapshot.Data = map[string]string{}
	}

	for _, rke2Chart := range rke2Charts {
		chartSnapshot, err := r.createHelmChartSnapshot(rke2Chart)
		if err != nil {
			return fmt.Errorf("parsing fingerprint from HelmChart '%s': %w", rke2Chart.Name, err)
		}
		data, err := json.Marshal(chartSnapshot)
		if err != nil {
			return fmt.Errorf("marshaling RKE2 HelmChart '%s' fingerprint: %w", rke2Chart.Name, err)
		}

		snapshot.Data[rke2Chart.Name] = string(data)
	}

	return nil
}

func (r *RKE2PackagedComponentsSnapshotter) createHelmChartSnapshot(chart helmv1.HelmChart) (*PackagedComponentChartSnapshot, error) {
	snapshot := &PackagedComponentChartSnapshot{
		Name:      chart.Name,
		Namespace: chart.Namespace,
	}

	if val, ok := chart.Annotations[chartURLAnnotation]; ok {
		snapshot.ChartURL = val
	}

	if chart.Spec.ChartContent != "" {
		snapshot.ChartContentSHA = hashContent(chart.Spec.ChartContent)
	}

	info, err := r.helmClient.RetrieveRelease(chart.Name)
	if err != nil {
		return nil, fmt.Errorf("retrieving Helm release '%s': %w", chart.Name, err)
	}

	snapshot.ReleaseRevisions = info.Revisions
	snapshot.ChartVersion = info.ChartVersion

	return snapshot, nil
}

func (r *RKE2PackagedComponentsSnapshotter) Load(ctx context.Context, nn types.NamespacedName) (*PackagedComponentsSnapshot, error) {
	logger := log.FromContext(ctx)
	logger.Info("Loading RKE2 packaged components snapshot",
		"name", nn.Name,
		"namespace", nn.Namespace)

	snapshot := &corev1.ConfigMap{}
	if err := r.Get(ctx, nn, snapshot); err != nil {
		return nil, fmt.Errorf("getting RKE2 packaged component snapshot: %w", err)
	}

	parsedSnapshot := &PackagedComponentsSnapshot{}
	for name, data := range snapshot.Data {
		parsedChartSnapshot := &PackagedComponentChartSnapshot{}
		if err := json.Unmarshal([]byte(data), parsedChartSnapshot); err != nil {
			return nil, fmt.Errorf("parsing RKE2 packaged component '%s': %w", name, err)
		}
		parsedSnapshot.Charts = append(parsedSnapshot.Charts, parsedChartSnapshot)
	}

	return parsedSnapshot, nil
}

func (r *RKE2PackagedComponentsSnapshotter) DeleteAndWait(ctx context.Context, nn types.NamespacedName, interval time.Duration, timeout time.Duration) error {
	logger := log.FromContext(ctx)
	logger.Info("Deleting RKE2 packaged components snapshot",
		"name", nn.Name,
		"namespace", nn.Namespace)

	snapshot := &corev1.ConfigMap{}
	if err := r.Get(ctx, nn, snapshot); apierrors.IsNotFound(err) {
		// Alredy deleted by another operation
		return nil
	} else if err != nil {
		return fmt.Errorf("retrieving RKE2 packaged component snapshot for deletion: %w", err)
	}

	if err := r.Delete(ctx, snapshot); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting RKE2 packaged component snapshot '%s': %w", snapshot.Name, err)
	}

	return wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (done bool, err error) {
		deleted := &corev1.ConfigMap{}
		getErr := r.Get(ctx, types.NamespacedName{Name: snapshot.Name, Namespace: snapshot.Namespace}, deleted)
		if apierrors.IsNotFound(getErr) {
			return true, nil
		}

		if getErr != nil {
			return false, getErr
		}
		return false, nil
	})
}

type chartSnapshotPair struct {
	Chart    *helmv1.HelmChart
	Snapshot *PackagedComponentChartSnapshot
}

type RKE2PackagedComponentsVerifier struct {
	client.Client
	helmClient     helm.Client
	findComponents func(ctx context.Context, client client.Client) (map[string]helmv1.HelmChart, error)
}

func NewRKE2PackagedComponentsVerifier(
	c client.Client,
	helm helm.Client,
	findComponents func(ctx context.Context, client client.Client) (map[string]helmv1.HelmChart, error),
) *RKE2PackagedComponentsVerifier {
	verifier := &RKE2PackagedComponentsVerifier{
		Client:         c,
		helmClient:     helm,
		findComponents: findComponents,
	}

	if verifier.findComponents == nil {
		verifier.findComponents = findRKE2PackagedComponents
	}

	return verifier
}

func (r *RKE2PackagedComponentsVerifier) VerifyAvailability(ctx context.Context, snapshot *PackagedComponentsSnapshot) (*PhaseStatus, error) {
	snapshotPairs, err := r.findNewOrChangedRKE2PackagedComponents(ctx, snapshot.Charts)
	if err != nil {
		return nil, fmt.Errorf("finding new or changed RKE2 packaged components: %w", err)
	}

	for _, pair := range snapshotPairs {
		jobComplete, err := r.isHelmChartJobComplete(ctx, pair)
		if err != nil {
			return nil, fmt.Errorf("validating job for RKE2 packaged component '%s': %w", pair.Chart.Name, err)
		}

		if !jobComplete {
			return &PhaseStatus{
				State:   lifecyclev1alpha1.UpgradeInProgress,
				Message: fmt.Sprintf("'%s' RKE2 HelmChart Job execution is still in progress", pair.Chart.Name),
			}, nil
		}
	}

	return &PhaseStatus{
		State:   lifecyclev1alpha1.K8sPackagedComponentsAvailable,
		Message: "All RKE2 packaged components available",
	}, nil
}

func (r *RKE2PackagedComponentsVerifier) findNewOrChangedRKE2PackagedComponents(ctx context.Context, chartSnapshots []*PackagedComponentChartSnapshot) ([]*chartSnapshotPair, error) {
	snapshotMap := make(map[string]*PackagedComponentChartSnapshot, len(chartSnapshots))
	for _, chart := range chartSnapshots {
		snapshotMap[chart.Name] = chart
	}

	latestComponents, err := r.findComponents(ctx, r.Client)
	if err != nil {
		return nil, fmt.Errorf("finding RKE2 packaged components: %w", err)
	}

	changedState := []*chartSnapshotPair{}
	newComponents := []*chartSnapshotPair{}
	for name, chart := range latestComponents {
		snap, ok := snapshotMap[name]
		if !ok {
			newComponents = append(newComponents, &chartSnapshotPair{Chart: &chart})
			continue
		}

		if r.chartStateChanged(&chart, snap) {
			changedState = append(changedState, &chartSnapshotPair{
				Chart:    &chart,
				Snapshot: snap,
			})
		}
	}

	return append(changedState, newComponents...), nil
}

func (r *RKE2PackagedComponentsVerifier) chartStateChanged(chart *helmv1.HelmChart, chartSnapshot *PackagedComponentChartSnapshot) bool {
	activeContent := hashContent(chart.Spec.ChartContent)
	return activeContent != chartSnapshot.ChartContentSHA ||
		chart.Annotations[chartURLAnnotation] != chartSnapshot.ChartURL
}

func (r *RKE2PackagedComponentsVerifier) isHelmChartJobComplete(ctx context.Context, pair *chartSnapshotPair) (bool, error) {
	chart := pair.Chart

	// Check if the upgrade job exists and is complete
	if chart.Status.JobName == "" {
		return false, nil
	}

	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      chart.Status.JobName,
		Namespace: chart.Namespace,
	}, job); err != nil {
		// Job might be cleaned up after completion, check actual helm release version
		if apierrors.IsNotFound(err) {
			return r.hasHelmReleaseAdvancedPastSnapshot(pair)
		}
		return false, err
	}

	// Check if job is complete
	isComplete := slices.ContainsFunc(job.Status.Conditions, func(c batchv1.JobCondition) bool {
		return c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue
	})

	if !isComplete {
		return false, nil
	}

	return r.hasHelmReleaseAdvancedPastSnapshot(pair)
}

func (r *RKE2PackagedComponentsVerifier) hasHelmReleaseAdvancedPastSnapshot(pair *chartSnapshotPair) (bool, error) {
	release, err := r.helmClient.RetrieveRelease(pair.Chart.Name)
	if err != nil {
		return false, fmt.Errorf("retrieving RKE2 packaged component Helm release %s: %w", pair.Chart.Name, err)
	}

	if pair.Snapshot != nil {
		return release.ChartVersion != pair.Snapshot.ChartVersion || release.Revisions > pair.Snapshot.ReleaseRevisions, nil
	}

	// New components do not have snapshots, as such assume that an available release is enough to consider the release
	// advanced past the snapshot
	return true, nil
}

func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func findRKE2PackagedComponents(ctx context.Context, c client.Client) (map[string]helmv1.HelmChart, error) {
	const (
		rke2HelmChartNS    = "kube-system"
		namePrefix         = "rke2-"
		ownerAnnotation    = "objectset.rio.cattle.io/owner-name"
		ownerGVKAnnotation = "objectset.rio.cattle.io/owner-gvk"
		addonOwner         = "k3s.cattle.io/v1, Kind=Addon"
	)

	var allCharts helmv1.HelmChartList
	if err := c.List(ctx, &allCharts, client.InNamespace(rke2HelmChartNS)); err != nil {
		return nil, fmt.Errorf("listing HelmChart resources in '%s' namespace: %w", rke2HelmChartNS, err)
	}

	found := make(map[string]helmv1.HelmChart, len(allCharts.Items))
	for _, chart := range allCharts.Items {
		if !strings.HasPrefix(chart.Name, namePrefix) {
			continue
		}

		annotations := chart.Annotations

		if _, ok := annotations[ownerAnnotation]; !ok {
			continue
		}

		if val, ok := annotations[ownerGVKAnnotation]; !ok || val != addonOwner {
			continue
		}

		if _, ok := annotations[chartURLAnnotation]; !ok {
			continue
		}

		found[chart.Name] = chart
	}

	return found, nil
}
