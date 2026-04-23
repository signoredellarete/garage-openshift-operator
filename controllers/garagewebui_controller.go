package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "github.com/garage-operator/garage-openshift-operator/api/v1alpha1"
)

const (
	webuiFinalizerName = "storage.garage.io/webui-finalizer"
	defaultWebuiImage  = "khairul169/garage-webui"
	webuiPort          = 3909
)

// GarageWebUIReconciler reconciles GarageWebUI objects
//
// +kubebuilder:rbac:groups=storage.garage.io,resources=garagewebuis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.garage.io,resources=garagewebuis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.garage.io,resources=garagewebuis/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
type GarageWebUIReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *GarageWebUIReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	webui := &storagev1alpha1.GarageWebUI{}
	if err := r.Get(ctx, req.NamespacedName, webui); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !webui.DeletionTimestamp.IsZero() {
		return r.handleWebuiDeletion(ctx, webui)
	}

	if !controllerutil.ContainsFinalizer(webui, webuiFinalizerName) {
		controllerutil.AddFinalizer(webui, webuiFinalizerName)
		if err := r.Update(ctx, webui); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Resolve the referenced GarageCluster
	cluster := &storagev1alpha1.GarageCluster{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      webui.Spec.GarageClusterRef.Name,
		Namespace: webui.Namespace,
	}, cluster); err != nil {
		logger.Error(err, "referenced GarageCluster not found", "cluster", webui.Spec.GarageClusterRef.Name)
		return ctrl.Result{RequeueAfter: 30 * time.Second},
			r.setWebuiCondition(ctx, webui, "Waiting", fmt.Sprintf("GarageCluster %q not found", webui.Spec.GarageClusterRef.Name))
	}

	if cluster.Status.Phase != "Ready" {
		logger.Info("waiting for GarageCluster to be Ready", "cluster", cluster.Name)
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	// Retrieve admin token from cluster secret
	adminToken, err := r.getAdminToken(ctx, cluster)
	if err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	for _, step := range []func(context.Context, *storagev1alpha1.GarageWebUI, *storagev1alpha1.GarageCluster, string) error{
		r.ensureWebuiDeployment,
		r.ensureWebuiService,
	} {
		if err := step(ctx, webui, cluster, adminToken); err != nil {
			logger.Error(err, "webui reconciliation step failed")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, err
		}
	}

	if err := r.ensureWebuiRoute(ctx, webui); err != nil {
		logger.Error(err, "failed to reconcile WebUI Route")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	// Auto-update check
	var requeueAfter time.Duration
	if webui.Spec.AutoUpdate.Enabled {
		if err := r.checkWebUIForUpdates(ctx, webui); err != nil {
			logger.Error(err, "webui update check failed")
		}
		requeueAfter = updateCheckInterval(webui.Spec.AutoUpdate.Schedule)
	}

	// Update status
	deploy := &appsv1.Deployment{}
	_ = r.Get(ctx, types.NamespacedName{Name: webui.Name, Namespace: webui.Namespace}, deploy)

	return ctrl.Result{RequeueAfter: requeueAfter}, r.updateWebuiStatus(ctx, webui, deploy)
}

// ── Deployment ─────────────────────────────────────────────────────────────────

func (r *GarageWebUIReconciler) ensureWebuiDeployment(ctx context.Context, webui *storagev1alpha1.GarageWebUI, cluster *storagev1alpha1.GarageCluster, adminToken string) error {
	image := webuiImage(webui)
	replicas := webui.Spec.Replicas

	adminEndpoint := fmt.Sprintf("http://%s-admin.%s.svc.cluster.local:3903", cluster.Name, cluster.Namespace)
	s3Endpoint := fmt.Sprintf("http://%s-s3.%s.svc.cluster.local:3900", cluster.Name, cluster.Namespace)
	s3Region := defaultStr(cluster.Spec.Config.S3Region, "garage")

	envVars := []corev1.EnvVar{
		{Name: "API_BASE_URL", Value: adminEndpoint},
		{Name: "API_ADMIN_KEY", Value: adminToken},
		{Name: "S3_ENDPOINT_URL", Value: s3Endpoint},
		{Name: "S3_REGION", Value: s3Region},
		{Name: "PORT", Value: fmt.Sprintf("%d", webuiPort)},
	}

	if webui.Spec.Auth != nil {
		// AUTH_USER_PASS is read from a Secret
		envVars = append(envVars, corev1.EnvVar{
			Name: "AUTH_USER_PASS",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: webui.Spec.Auth.SecretRef.Name},
					Key:                  webui.Spec.Auth.SecretRef.Key,
				},
			},
		})
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      webui.Name,
			Namespace: webui.Namespace,
			Labels:    webuiLabels(webui),
		},
	}
	if err := controllerutil.SetControllerReference(webui, deploy, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: webuiSelectorLabels(webui),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: webuiLabels(webui)},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "garage-webui",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: webuiPort, Protocol: corev1.ProtocolTCP},
							},
							Env:       envVars,
							Resources: webui.Spec.Resources,
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(webuiPort)},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
						},
					},
				},
			},
		}
		return nil
	})
}

// ── Service ────────────────────────────────────────────────────────────────────

func (r *GarageWebUIReconciler) ensureWebuiService(ctx context.Context, webui *storagev1alpha1.GarageWebUI, _ *storagev1alpha1.GarageCluster, _ string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      webui.Name,
			Namespace: webui.Namespace,
			Labels:    webuiLabels(webui),
		},
		Spec: corev1.ServiceSpec{
			Selector: webuiSelectorLabels(webui),
			Ports: []corev1.ServicePort{
				{Name: "http", Port: webuiPort, TargetPort: intstr.FromInt(webuiPort), Protocol: corev1.ProtocolTCP},
			},
		},
	}
	if err := controllerutil.SetControllerReference(webui, svc, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, svc, func() error { return nil })
}

// ── Route ──────────────────────────────────────────────────────────────────────

func (r *GarageWebUIReconciler) ensureWebuiRoute(ctx context.Context, webui *storagev1alpha1.GarageWebUI) error {
	if !webui.Spec.Expose.Route.Enabled {
		return nil
	}

	termination := "Edge"
	switch webui.Spec.Expose.Route.TLSTermination {
	case "passthrough":
		termination = "Passthrough"
	case "reencrypt":
		termination = "Reencrypt"
	}

	route := &unstructured.Unstructured{}
	route.SetAPIVersion("route.openshift.io/v1")
	route.SetKind("Route")
	route.SetName(webui.Name)
	route.SetNamespace(webui.Namespace)
	route.SetLabels(webuiLabels(webui))

	if err := controllerutil.SetControllerReference(webui, route, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, route, func() error {
		route.Object["spec"] = map[string]interface{}{
			"host": webui.Spec.Expose.Route.Hostname,
			"to": map[string]interface{}{
				"kind":   "Service",
				"name":   webui.Name,
				"weight": int64(100),
			},
			"port": map[string]interface{}{
				"targetPort": int64(webuiPort),
			},
			"tls": map[string]interface{}{
				"termination":                   termination,
				"insecureEdgeTerminationPolicy": "Redirect",
			},
		}
		return nil
	})
}

// ── Status helpers ─────────────────────────────────────────────────────────────

func (r *GarageWebUIReconciler) updateWebuiStatus(ctx context.Context, webui *storagev1alpha1.GarageWebUI, deploy *appsv1.Deployment) error {
	patch := client.MergeFrom(webui.DeepCopy())

	webui.Status.CurrentVersion = webui.Spec.Version
	webui.Status.ReadyReplicas = deploy.Status.ReadyReplicas

	if deploy.Status.ReadyReplicas > 0 {
		webui.Status.Phase = "Ready"
	} else {
		webui.Status.Phase = "Provisioning"
	}

	// Retrieve route URL if available
	if webui.Spec.Expose.Route.Enabled {
		route := &unstructured.Unstructured{}
		route.SetAPIVersion("route.openshift.io/v1")
		route.SetKind("Route")
		if err := r.Get(ctx, types.NamespacedName{Name: webui.Name, Namespace: webui.Namespace}, route); err == nil {
			if spec, ok := route.Object["spec"].(map[string]interface{}); ok {
				if host, ok := spec["host"].(string); ok && host != "" {
					webui.Status.URL = "https://" + host
				}
			}
		}
	}

	return r.Status().Patch(ctx, webui, patch)
}

func (r *GarageWebUIReconciler) setWebuiCondition(ctx context.Context, webui *storagev1alpha1.GarageWebUI, phase, msg string) error {
	patch := client.MergeFrom(webui.DeepCopy())
	webui.Status.Phase = phase
	r.Recorder.Event(webui, corev1.EventTypeWarning, phase, msg)
	return r.Status().Patch(ctx, webui, patch)
}

// ── Deletion ───────────────────────────────────────────────────────────────────

func (r *GarageWebUIReconciler) handleWebuiDeletion(ctx context.Context, webui *storagev1alpha1.GarageWebUI) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(webui, webuiFinalizerName) {
		controllerutil.RemoveFinalizer(webui, webuiFinalizerName)
		if err := r.Update(ctx, webui); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// ── SetupWithManager ───────────────────────────────────────────────────────────

func (r *GarageWebUIReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.GarageWebUI{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func webuiLabels(webui *storagev1alpha1.GarageWebUI) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "garage-webui",
		"app.kubernetes.io/instance":   webui.Name,
		"app.kubernetes.io/managed-by": "garage-operator",
	}
}

func webuiSelectorLabels(webui *storagev1alpha1.GarageWebUI) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "garage-webui",
		"app.kubernetes.io/instance": webui.Name,
	}
}

func webuiImage(webui *storagev1alpha1.GarageWebUI) string {
	if webui.Spec.Image != "" {
		return webui.Spec.Image
	}
	return fmt.Sprintf("%s:%s", defaultWebuiImage, webui.Spec.Version)
}

func (r *GarageWebUIReconciler) getAdminToken(ctx context.Context, cluster *storagev1alpha1.GarageCluster) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      cluster.Name + "-secrets",
		Namespace: cluster.Namespace,
	}, secret); err != nil {
		return "", fmt.Errorf("fetching cluster secrets: %w", err)
	}
	return string(secret.Data["admin-token"]), nil
}
