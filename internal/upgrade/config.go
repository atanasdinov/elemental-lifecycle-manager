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
	"fmt"

	"github.com/suse/elemental/v3/pkg/manifest/api"
	"github.com/suse/elemental/v3/pkg/manifest/resolver"
)

// Config represents a complete upgrade specification for all phases.
type Config struct {
	// ReleaseName is the name of the Release resource.
	ReleaseName string
	// Version is the target release version.
	Version string
	// OS contains the SUC Plan configuration for OS upgrades.
	OS *OSConfig
	// Kubernetes contains the SUC Plan configuration for Kubernetes upgrades.
	Kubernetes *KubernetesConfig
	// HelmCharts contains the Helm charts to deploy via Helm Controller.
	HelmCharts *HelmChartConfig
}

// OSConfig contains configuration for upgrading the operating system.
type OSConfig struct {
	// Image is the target image for the upgrade.
	Image string
	// Version is the target version.
	Version string
	// TODO: Populate this from the manifest.
	PrettyName string
	// DrainOpts specifies which nodes should be drained before operating system upgrades.
	DrainOpts *DrainOpts
}

// KubernetesConfig contains configuration for upgrading the Kubernetes version.
type KubernetesConfig struct {
	// Image is the target image for the upgrade.
	Image string
	// Version is the target version.
	Version string
	// CoreComponents lists additional components that must be verified after node upgrades.
	// Used to verify RKE2 components (CoreDNS, ingress, etc.) are ready.
	CoreComponents []CoreComponent
	// DrainOpts specifies which nodes should be drained before Kubernetes upgrades.
	DrainOpts *DrainOpts
}

// DrainOpts contains options for draining specific node types
type DrainOpts struct {
	// ControlPlane specifies that control plane nodes need to be drained
	ControlPlane bool
	// Worker specifies that worker nodes need to be drained
	Worker bool
}

// CoreComponentType identifies the type of Kubernetes core component.
type CoreComponentType string

const (
	// CoreComponentHelmChart indicates the component is managed by a HelmChart resource.
	CoreComponentHelmChart CoreComponentType = "HelmChart"
	// CoreComponentDeployment indicates the component is a Deployment.
	CoreComponentDeployment CoreComponentType = "Deployment"
)

// CoreComponent represents a Kubernetes core component that must be verified during upgrades.
// These are components bundled with the Kubernetes distribution (e.g., RKE2) that may still
// be upgrading even after nodes report the correct kubelet version.
type CoreComponent struct {
	// Name is the resource name of the component.
	Name string
	// Type is the kind of resource (HelmChart or Deployment).
	Type CoreComponentType
	// Version is the expected version after upgrade.
	Version string
	// Containers maps container names to expected images (used for Deployment type).
	Containers map[string]string
}

// HelmChartConfig contains configuration for Helm Controller HelmChart resources.
type HelmChartConfig struct {
	// Charts is the list of Helm charts to deploy/upgrade.
	Charts []*api.HelmChart
	// Repositories is the list of Helm repositories.
	Repositories []*api.HelmRepository
}

// NewConfig creates a release upgrade specification from the resolved manifest.
// The upgrade is built by extracting configuration from the core platform
// and optionally merging with product extension components.
func NewConfig(manifest *resolver.ResolvedManifest, releaseName string, drainOpts *DrainOpts) (*Config, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest is nil")
	}

	if manifest.CorePlatform == nil {
		return nil, fmt.Errorf("core platform manifest is required")
	}

	core := manifest.CorePlatform
	config := &Config{
		ReleaseName: releaseName,
		Version:     core.Metadata.Version,
		OS: &OSConfig{
			Image:     core.Components.OperatingSystem.Image.Base,
			Version:   core.Metadata.Version,
			DrainOpts: drainOpts,
		},
	}

	config.Kubernetes = &KubernetesConfig{
		Image:   core.Components.Kubernetes.Image,
		Version: core.Components.Kubernetes.Version,
		// TODO: Populate CoreComponents from the release manifest
		CoreComponents: nil,
		DrainOpts:      drainOpts,
	}

	if manifest.ProductExtension == nil {
		config.HelmCharts = helmChartConfig(core.Components.Helm, nil)
	} else {
		product := manifest.ProductExtension
		config.HelmCharts = helmChartConfig(core.Components.Helm, product.Components.Helm)
	}

	return config, nil
}

// helmChartConfig merges Helm configurations from core and product manifests.
func helmChartConfig(core, product *api.Helm) *HelmChartConfig {
	config := &HelmChartConfig{
		Charts:       make([]*api.HelmChart, 0),
		Repositories: make([]*api.HelmRepository, 0),
	}

	// Add core charts and repositories
	if core != nil {
		config.Charts = append(config.Charts, core.Charts...)
		config.Repositories = append(config.Repositories, core.Repositories...)
	}

	// Add product charts and repositories
	if product != nil {
		config.Charts = append(config.Charts, product.Charts...)
		config.Repositories = append(config.Repositories, product.Repositories...)
	}

	if len(config.Charts) == 0 && len(config.Repositories) == 0 {
		return nil
	}

	return config
}
