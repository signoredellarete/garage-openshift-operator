package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "github.com/garage-operator/garage-openshift-operator/api/v1alpha1"
)

const (
	garageGitHubRepo  = "deuxfleurs-org/garage"
	webuiGitHubRepo   = "khairul169/garage-webui"
	githubReleasesAPI = "https://api.github.com/repos/%s/releases/latest"
)

// githubRelease is the partial response from the GitHub releases API
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// fetchLatestVersion queries GitHub for the latest release of a repository.
// Returns the tag name (e.g. "v1.0.1").
func fetchLatestVersion(ctx context.Context, repo string, allowPreRelease bool) (string, error) {
	url := fmt.Sprintf(githubReleasesAPI, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "garage-openshift-operator/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return "", fmt.Errorf("GitHub API rate limit exceeded (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned unexpected status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding GitHub response: %w", err)
	}

	if release.Draft {
		return "", fmt.Errorf("latest release is a draft")
	}
	if release.Prerelease && !allowPreRelease {
		return "", fmt.Errorf("latest release %s is a pre-release (allowPreRelease=false)", release.TagName)
	}
	return release.TagName, nil
}

// isNewer returns true when candidate is a newer semantic version than current.
// Both strings must have a leading "v" (e.g. "v1.0.1").
func isNewer(current, candidate string) bool {
	if current == "" || candidate == "" {
		return false
	}
	return semverGT(candidate, current)
}

// semverGT returns true if a > b (both in "vMAJOR.MINOR.PATCH[-pre]" form).
func semverGT(a, b string) bool {
	aParts := splitSemver(a)
	bParts := splitSemver(b)
	for i := 0; i < 3 && i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] > bParts[i] {
			return true
		}
		if aParts[i] < bParts[i] {
			return false
		}
	}
	return false
}

func splitSemver(v string) []int {
	v = strings.TrimPrefix(v, "v")
	// Strip pre-release suffix (e.g. "-rc1")
	if idx := strings.Index(v, "-"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 3)
	for i, p := range parts {
		if i >= 3 {
			break
		}
		n := 0
		for _, c := range p {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		nums[i] = n
	}
	return nums
}

// ── GarageCluster auto-update ─────────────────────────────────────────────────

// checkForUpdates queries GitHub for a newer Garage version and, if found,
// updates the GarageCluster spec and status accordingly.
func (r *GarageClusterReconciler) checkForUpdates(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	logger := log.FromContext(ctx)

	// Throttle: skip if last check was less than 1h ago
	if cluster.Status.LastUpdateCheck != nil {
		if time.Since(cluster.Status.LastUpdateCheck.Time) < time.Hour {
			return nil
		}
	}

	latest, err := fetchLatestVersion(ctx, garageGitHubRepo, cluster.Spec.AutoUpdate.AllowPreRelease)
	if err != nil {
		logger.V(1).Info("version check failed", "error", err)
		return nil // non-fatal: log and move on
	}

	now := metav1.Now()
	patch := client.MergeFrom(cluster.DeepCopy())
	cluster.Status.LastUpdateCheck = &now
	cluster.Status.AvailableVersion = latest

	if isNewer(cluster.Spec.Version, latest) {
		logger.Info("new Garage version available", "current", cluster.Spec.Version, "latest", latest)
		r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "UpdateAvailable",
			"New Garage version available: %s (current: %s)", latest, cluster.Spec.Version)

		// Auto-upgrade: bump spec.version; the reconciler will roll out the new image
		cluster.Spec.Version = latest
		if updateErr := r.Update(ctx, cluster); updateErr != nil {
			return fmt.Errorf("updating GarageCluster version: %w", updateErr)
		}
		r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "AutoUpdate",
			"Initiated rolling update to Garage %s", latest)
	}

	return r.Status().Patch(ctx, cluster, patch)
}

// ── GarageWebUI auto-update ───────────────────────────────────────────────────

// checkWebUIForUpdates queries GitHub for a newer garage-webui version.
func (r *GarageWebUIReconciler) checkWebUIForUpdates(ctx context.Context, webui *storagev1alpha1.GarageWebUI) error {
	logger := log.FromContext(ctx)

	if webui.Status.LastUpdateCheck != nil {
		if time.Since(webui.Status.LastUpdateCheck.Time) < time.Hour {
			return nil
		}
	}

	latest, err := fetchLatestVersion(ctx, webuiGitHubRepo, webui.Spec.AutoUpdate.AllowPreRelease)
	if err != nil {
		logger.V(1).Info("webui version check failed", "error", err)
		return nil
	}

	now := metav1.Now()
	patch := client.MergeFrom(webui.DeepCopy())
	webui.Status.LastUpdateCheck = &now
	webui.Status.AvailableVersion = latest

	if isNewer(webui.Spec.Version, latest) {
		logger.Info("new garage-webui version available", "current", webui.Spec.Version, "latest", latest)
		r.Recorder.Eventf(webui, corev1.EventTypeNormal, "UpdateAvailable",
			"New garage-webui version available: %s (current: %s)", latest, webui.Spec.Version)

		webui.Spec.Version = latest
		if updateErr := r.Update(ctx, webui); updateErr != nil {
			return fmt.Errorf("updating GarageWebUI version: %w", updateErr)
		}
		r.Recorder.Eventf(webui, corev1.EventTypeNormal, "AutoUpdate",
			"Initiated rolling update to garage-webui %s", latest)
	}

	return r.Status().Patch(ctx, webui, patch)
}
