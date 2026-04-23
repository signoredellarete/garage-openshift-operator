package controllers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "github.com/garage-operator/garage-openshift-operator/api/v1alpha1"
)

const (
	garageFinalizerName = "storage.garage.io/finalizer"
	defaultGarageImage  = "dxflrs/garage"
	garageMetaDir       = "/var/lib/garage/meta"
	garageDataDir       = "/var/lib/garage/data"
	conditionReady      = "Ready"
)

// GarageClusterReconciler reconciles GarageCluster objects
//
// +kubebuilder:rbac:groups=storage.garage.io,resources=garageclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.garage.io,resources=garageclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.garage.io,resources=garageclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups=core,resources=services;configmaps;secrets;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
type GarageClusterReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	RESTConfig *rest.Config
}

func (r *GarageClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cluster := &storagev1alpha1.GarageCluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cluster.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, cluster)
	}

	if !controllerutil.ContainsFinalizer(cluster, garageFinalizerName) {
		controllerutil.AddFinalizer(cluster, garageFinalizerName)
		if err := r.Update(ctx, cluster); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	steps := []func(context.Context, *storagev1alpha1.GarageCluster) error{
		r.ensureSecrets,
		r.ensureConfigMap,
		r.ensureServiceAccount,
		r.ensureRBAC,
		r.ensureHeadlessService,
		r.ensureServices,
		r.ensureStatefulSet,
	}
	for _, step := range steps {
		if err := step(ctx, cluster); err != nil {
			logger.Error(err, "reconciliation step failed")
			return ctrl.Result{RequeueAfter: 15 * time.Second},
				r.setDegradedCondition(ctx, cluster, err.Error())
		}
	}

	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, sts); err != nil {
		return ctrl.Result{}, err
	}

	ready := sts.Status.ReadyReplicas == cluster.Spec.Replicas

	if ready && !cluster.Status.LayoutApplied {
		if err := r.initLayout(ctx, cluster); err != nil {
			logger.Error(err, "layout initialisation failed, will retry")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	if err := r.ensureRoutes(ctx, cluster); err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	var requeueAfter time.Duration
	if cluster.Spec.AutoUpdate.Enabled {
		if err := r.checkForUpdates(ctx, cluster); err != nil {
			logger.Error(err, "update check failed")
		}
		requeueAfter = updateCheckInterval(cluster.Spec.AutoUpdate.Schedule)
	}

	phase := "Provisioning"
	if ready && cluster.Status.LayoutApplied {
		phase = "Ready"
	}
	return ctrl.Result{RequeueAfter: requeueAfter},
		r.updateStatus(ctx, cluster, phase, sts.Status.ReadyReplicas)
}

// ── Secrets ────────────────────────────────────────────────────────────────────

func (r *GarageClusterReconciler) ensureSecrets(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	name := cluster.Name + "-secrets"
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: cluster.Namespace}, existing)
	if err == nil {
		return nil // already exists, leave as-is to preserve generated values
	}
	if !errors.IsNotFound(err) {
		return err
	}

	rpcSecret, err := randomHex(32)
	if err != nil {
		return err
	}
	adminToken, err := randomHex(16)
	if err != nil {
		return err
	}

	// Override with user-provided secrets if specified
	if cluster.Spec.RPCSecretRef != nil {
		if v, readErr := r.readSecretKey(ctx, cluster.Namespace, cluster.Spec.RPCSecretRef); readErr == nil {
			rpcSecret = v
		}
	}
	if cluster.Spec.AdminTokenRef != nil {
		if v, readErr := r.readSecretKey(ctx, cluster.Namespace, cluster.Spec.AdminTokenRef); readErr == nil {
			adminToken = v
		}
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labelsFor(cluster),
		},
		StringData: map[string]string{
			"rpc-secret":  rpcSecret,
			"admin-token": adminToken,
		},
	}
	if err := controllerutil.SetControllerReference(cluster, secret, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, secret)
}

func (r *GarageClusterReconciler) readSecretKey(ctx context.Context, ns string, ref *storagev1alpha1.SecretKeyRef) (string, error) {
	s := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, s); err != nil {
		return "", err
	}
	return string(s.Data[ref.Key]), nil
}

// ── ConfigMap (garage.toml) ────────────────────────────────────────────────────

const garageTOMLTemplate = `metadata_dir = "{{ .MetaDir }}"
data_dir     = "{{ .DataDir }}"
db_engine    = "{{ .DBEngine }}"

replication_factor = {{ .ReplicationFactor }}
consistency_mode   = "{{ .ConsistencyMode }}"
{{ if .BlockSize }}block_size = "{{ .BlockSize }}"{{ end }}
{{ if gt .CompressionLevel 0 }}compression_level = {{ .CompressionLevel }}{{ end }}

rpc_bind_addr = "0.0.0.0:3901"

[kubernetes_discovery]
  namespace    = "{{ .Namespace }}"
  service_name = "{{ .ServiceName }}"
  skip_crd     = false

[s3_api]
  api_bind_addr = "0.0.0.0:3900"
  s3_region     = "{{ .S3Region }}"
  {{ if .S3RootDomain }}root_domain = "{{ .S3RootDomain }}"{{ end }}

[s3_web]
  bind_addr = "0.0.0.0:3902"
  {{ if .WebRootDomain }}root_domain = "{{ .WebRootDomain }}"{{ end }}

[admin]
  api_bind_addr = "0.0.0.0:3903"
`

type garageTOMLData struct {
	MetaDir, DataDir, DBEngine, ConsistencyMode string
	ReplicationFactor                           int
	BlockSize                                   string
	CompressionLevel                            int
	Namespace, ServiceName                      string
	S3Region, S3RootDomain, WebRootDomain       string
}

func (r *GarageClusterReconciler) ensureConfigMap(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	cfg := cluster.Spec.Config
	data := garageTOMLData{
		MetaDir:           garageMetaDir,
		DataDir:           garageDataDir,
		DBEngine:          defaultStr(cfg.DBEngine, "lmdb"),
		ReplicationFactor: defaultInt(cfg.ReplicationFactor, 1),
		ConsistencyMode:   defaultStr(cfg.ConsistencyMode, "consistent"),
		BlockSize:         cfg.BlockSize,
		CompressionLevel:  cfg.CompressionLevel,
		Namespace:         cluster.Namespace,
		ServiceName:       cluster.Name,
		S3Region:          defaultStr(cfg.S3Region, "garage"),
		S3RootDomain:      cfg.S3RootDomain,
		WebRootDomain:     cfg.WebRootDomain,
	}

	tmpl, err := template.New("garage.toml").Parse(garageTOMLTemplate)
	if err != nil {
		return fmt.Errorf("parsing garage.toml template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering garage.toml: %w", err)
	}
	rendered := buf.String()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + "-config",
			Namespace: cluster.Namespace,
			Labels:    labelsFor(cluster),
		},
	}
	if err := controllerutil.SetControllerReference(cluster, cm, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, cm, func() error {
		cm.Data = map[string]string{"garage.toml": rendered}
		return nil
	})
}

// ── ServiceAccount + RBAC for Garage pods ─────────────────────────────────────

func (r *GarageClusterReconciler) ensureServiceAccount(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
			Labels:    labelsFor(cluster),
		},
	}
	if err := controllerutil.SetControllerReference(cluster, sa, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, sa, func() error { return nil })
}

func (r *GarageClusterReconciler) ensureRBAC(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	roleName := cluster.Name + "-garage"
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: cluster.Namespace, Labels: labelsFor(cluster)},
	}
	if err := controllerutil.SetControllerReference(cluster, role, r.Scheme); err != nil {
		return err
	}
	if err := createOrUpdate(ctx, r.Client, role, func() error {
		role.Rules = []rbacv1.PolicyRule{{
			APIGroups: []string{"deuxfleurs.fr"},
			Resources: []string{"garagenodes"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		}}
		return nil
	}); err != nil {
		return err
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: cluster.Namespace, Labels: labelsFor(cluster)},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: cluster.Name, Namespace: cluster.Namespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: roleName},
	}
	if err := controllerutil.SetControllerReference(cluster, rb, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, rb, func() error { return nil })
}

// ── Services ───────────────────────────────────────────────────────────────────

func (r *GarageClusterReconciler) ensureHeadlessService(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + "-headless",
			Namespace: cluster.Namespace,
			Labels:    labelsFor(cluster),
		},
	}
	if err := controllerutil.SetControllerReference(cluster, svc, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec = corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  selectorFor(cluster),
			Ports:     []corev1.ServicePort{{Name: "rpc", Port: 3901, TargetPort: intstr.FromInt(3901), Protocol: corev1.ProtocolTCP}},
		}
		return nil
	})
}

func (r *GarageClusterReconciler) ensureServices(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	for _, s := range []struct{ name string; port int32 }{
		{cluster.Name + "-s3", 3900},
		{cluster.Name + "-web", 3902},
		{cluster.Name + "-admin", 3903},
	} {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: cluster.Namespace, Labels: labelsFor(cluster)},
		}
		port := s.port
		if err := controllerutil.SetControllerReference(cluster, svc, r.Scheme); err != nil {
			return err
		}
		if err := createOrUpdate(ctx, r.Client, svc, func() error {
			svc.Spec = corev1.ServiceSpec{
				Selector: selectorFor(cluster),
				Ports:    []corev1.ServicePort{{Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP}},
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// ── StatefulSet ────────────────────────────────────────────────────────────────

func (r *GarageClusterReconciler) ensureStatefulSet(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	image := garageImage(cluster)
	replicas := cluster.Spec.Replicas
	mode := int32(0444)

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, Labels: labelsFor(cluster)},
	}
	if err := controllerutil.SetControllerReference(cluster, sts, r.Scheme); err != nil {
		return err
	}

	existing := &appsv1.StatefulSet{}
	getErr := r.Get(ctx, types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, existing)
	if getErr != nil && !errors.IsNotFound(getErr) {
		return getErr
	}

	if errors.IsNotFound(getErr) {
		// Full spec only on first create (VolumeClaimTemplates are immutable)
		sts.Spec = appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: cluster.Name + "-headless",
			Selector:    &metav1.LabelSelector{MatchLabels: selectorFor(cluster)},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				buildPVC("meta", cluster.Spec.Storage.MetaStorageSize, cluster.Spec.Storage.StorageClassName),
				buildPVC("data", cluster.Spec.Storage.DataStorageSize, cluster.Spec.Storage.StorageClassName),
			},
			Template: r.buildPodTemplate(cluster, image, mode),
		}
		return r.Create(ctx, sts)
	}

	// On update: only touch replicas, image, resources (VolumeClaimTemplates are immutable)
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec.Replicas = &replicas
	existing.Spec.Template = r.buildPodTemplate(cluster, image, mode)
	return r.Patch(ctx, existing, patch)
}

func (r *GarageClusterReconciler) buildPodTemplate(cluster *storagev1alpha1.GarageCluster, image string, mode int32) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labelsFor(cluster)},
		Spec: corev1.PodSpec{
			ServiceAccountName: cluster.Name,
			NodeSelector:       cluster.Spec.NodeSelector,
			Tolerations:        cluster.Spec.Tolerations,
			Containers: []corev1.Container{{
				Name:            "garage",
				Image:           image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/garage"},
				Args:            []string{"-c", "/etc/garage/garage.toml", "server"},
				Ports: []corev1.ContainerPort{
					{Name: "s3", ContainerPort: 3900},
					{Name: "rpc", ContainerPort: 3901},
					{Name: "web", ContainerPort: 3902},
					{Name: "admin", ContainerPort: 3903},
				},
				Env: []corev1.EnvVar{
					envFromSecret("GARAGE_RPC_SECRET", cluster.Name+"-secrets", "rpc-secret"),
					envFromSecret("GARAGE_ADMIN_TOKEN", cluster.Name+"-secrets", "admin-token"),
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "config", MountPath: "/etc/garage", ReadOnly: true},
					{Name: "meta", MountPath: garageMetaDir},
					{Name: "data", MountPath: garageDataDir},
				},
				Resources: cluster.Spec.Resources,
				ReadinessProbe: &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(3900)}},
					InitialDelaySeconds: 10,
					PeriodSeconds:       10,
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(3901)}},
					InitialDelaySeconds: 30,
					PeriodSeconds:       20,
				},
			}},
			Volumes: []corev1.Volume{{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: cluster.Name + "-config"},
						DefaultMode:          &mode,
					},
				},
			}},
		},
	}
}

func buildPVC(name string, size resource.Quantity, storageClass string) corev1.PersistentVolumeClaim {
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
		},
	}
	if storageClass != "" {
		pvc.Spec.StorageClassName = &storageClass
	}
	return pvc
}

// ── Layout initialisation ─────────────────────────────────────────────────────

func (r *GarageClusterReconciler) initLayout(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	logger := log.FromContext(ctx)
	pod0 := cluster.Name + "-0"

	stdout, _, err := r.execInPod(ctx, cluster.Namespace, pod0, "garage",
		[]string{"garage", "status"})
	if err != nil {
		return fmt.Errorf("garage status: %w", err)
	}

	nodeIDs := parseNodeIDs(stdout)
	if len(nodeIDs) == 0 {
		return fmt.Errorf("no Garage nodes found in status output")
	}

	zone := defaultStr(cluster.Spec.Config.Zone, "dc1")
	capacityGB := dataSizeGB(cluster.Spec.Storage.DataStorageSize)

	for _, id := range nodeIDs {
		_, stderr, execErr := r.execInPod(ctx, cluster.Namespace, pod0, "garage",
			[]string{"garage", "layout", "assign", "-z", zone, "-c", fmt.Sprintf("%dG", capacityGB), id})
		if execErr != nil {
			return fmt.Errorf("layout assign %s: %w (stderr: %s)", id, execErr, stderr)
		}
	}

	if _, stderr, execErr := r.execInPod(ctx, cluster.Namespace, pod0, "garage",
		[]string{"garage", "layout", "apply", "--version", "1"}); execErr != nil {
		return fmt.Errorf("layout apply: %w (stderr: %s)", execErr, stderr)
	}

	logger.Info("Garage layout applied", "nodes", len(nodeIDs), "zone", zone)
	r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "LayoutApplied",
		"Garage cluster layout applied: %d nodes in zone %s", len(nodeIDs), zone)

	patch := client.MergeFrom(cluster.DeepCopy())
	cluster.Status.LayoutApplied = true
	return r.Status().Patch(ctx, cluster, patch)
}

// execInPod runs a command inside a container and returns stdout, stderr.
func (r *GarageClusterReconciler) execInPod(ctx context.Context, namespace, podName, containerName string, command []string) (string, string, error) {
	clientset, err := kubernetes.NewForConfig(r.RESTConfig)
	if err != nil {
		return "", "", fmt.Errorf("building clientset: %w", err)
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, clientgoscheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(r.RESTConfig, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("creating SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.String(), stderr.String(), err
}

// ── Routes ─────────────────────────────────────────────────────────────────────

func (r *GarageClusterReconciler) ensureRoutes(ctx context.Context, cluster *storagev1alpha1.GarageCluster) error {
	type routeDef struct {
		spec        storagev1alpha1.RouteSpec
		routeName   string
		serviceName string
		port        int32
	}
	expose := cluster.Spec.Expose
	for _, d := range []routeDef{
		{expose.S3APIRoute, cluster.Name + "-s3", cluster.Name + "-s3", 3900},
		{expose.WebRoute, cluster.Name + "-web", cluster.Name + "-web", 3902},
		{expose.AdminRoute, cluster.Name + "-admin", cluster.Name + "-admin", 3903},
	} {
		if !d.spec.Enabled {
			continue
		}
		if err := r.ensureRoute(ctx, cluster, d.routeName, d.serviceName, d.port, d.spec); err != nil {
			return err
		}
	}
	return nil
}

func (r *GarageClusterReconciler) ensureRoute(ctx context.Context, cluster *storagev1alpha1.GarageCluster, routeName, serviceName string, port int32, spec storagev1alpha1.RouteSpec) error {
	termination := "Edge"
	switch spec.TLSTermination {
	case "passthrough":
		termination = "Passthrough"
	case "reencrypt":
		termination = "Reencrypt"
	}

	route := &unstructured.Unstructured{}
	route.SetAPIVersion("route.openshift.io/v1")
	route.SetKind("Route")
	route.SetName(routeName)
	route.SetNamespace(cluster.Namespace)
	route.SetLabels(labelsFor(cluster))

	if err := controllerutil.SetControllerReference(cluster, route, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, route, func() error {
		route.Object["spec"] = map[string]interface{}{
			"host": spec.Hostname,
			"to": map[string]interface{}{
				"kind":   "Service",
				"name":   serviceName,
				"weight": int64(100),
			},
			"port": map[string]interface{}{
				"targetPort": port,
			},
			"tls": map[string]interface{}{
				"termination": termination,
			},
		}
		return nil
	})
}

// ── Deletion ───────────────────────────────────────────────────────────────────

func (r *GarageClusterReconciler) handleDeletion(ctx context.Context, cluster *storagev1alpha1.GarageCluster) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(cluster, garageFinalizerName) {
		controllerutil.RemoveFinalizer(cluster, garageFinalizerName)
		if err := r.Update(ctx, cluster); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// ── Status ─────────────────────────────────────────────────────────────────────

func (r *GarageClusterReconciler) updateStatus(ctx context.Context, cluster *storagev1alpha1.GarageCluster, phase string, readyReplicas int32) error {
	patch := client.MergeFrom(cluster.DeepCopy())
	cluster.Status.Phase = phase
	cluster.Status.ReadyReplicas = readyReplicas
	cluster.Status.CurrentVersion = cluster.Spec.Version
	cluster.Status.S3Endpoint = fmt.Sprintf("http://%s-s3.%s.svc.cluster.local:3900", cluster.Name, cluster.Namespace)
	cluster.Status.AdminEndpoint = fmt.Sprintf("http://%s-admin.%s.svc.cluster.local:3903", cluster.Name, cluster.Namespace)
	setCondition(cluster, conditionReady, phase == "Ready")
	return r.Status().Patch(ctx, cluster, patch)
}

func (r *GarageClusterReconciler) setDegradedCondition(ctx context.Context, cluster *storagev1alpha1.GarageCluster, msg string) error {
	patch := client.MergeFrom(cluster.DeepCopy())
	cluster.Status.Phase = "Degraded"
	setCondition(cluster, conditionReady, false)
	r.Recorder.Event(cluster, corev1.EventTypeWarning, "Degraded", msg)
	return r.Status().Patch(ctx, cluster, patch)
}

func setCondition(cluster *storagev1alpha1.GarageCluster, condType string, status bool) {
	condStatus := metav1.ConditionFalse
	if status {
		condStatus = metav1.ConditionTrue
	}
	now := metav1.Now()
	for i, c := range cluster.Status.Conditions {
		if c.Type == condType {
			cluster.Status.Conditions[i].Status = condStatus
			cluster.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             condStatus,
		LastTransitionTime: now,
		Reason:             condType,
	})
}

// ── SetupWithManager ───────────────────────────────────────────────────────────

func (r *GarageClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.GarageCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// ── Shared utilities ───────────────────────────────────────────────────────────

func labelsFor(cluster *storagev1alpha1.GarageCluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "garage",
		"app.kubernetes.io/instance":   cluster.Name,
		"app.kubernetes.io/managed-by": "garage-operator",
	}
}

func selectorFor(cluster *storagev1alpha1.GarageCluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "garage",
		"app.kubernetes.io/instance": cluster.Name,
	}
}

func garageImage(cluster *storagev1alpha1.GarageCluster) string {
	if cluster.Spec.Image != "" {
		return cluster.Spec.Image
	}
	return fmt.Sprintf("%s:%s", defaultGarageImage, cluster.Spec.Version)
}

// createOrUpdate gets the object, creates it if not found, or updates it.
// The mutate function is called before both Create and Update.
func createOrUpdate(ctx context.Context, c client.Client, obj client.Object, mutate func() error) error {
	key := types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}
	existing := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, key, existing)
	if errors.IsNotFound(err) {
		if mutateErr := mutate(); mutateErr != nil {
			return mutateErr
		}
		return c.Create(ctx, obj)
	}
	if err != nil {
		return err
	}
	if mutateErr := mutate(); mutateErr != nil {
		return mutateErr
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return c.Update(ctx, obj)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func defaultInt(i, def int) int {
	if i == 0 {
		return def
	}
	return i
}

func envFromSecret(envName, secretName, secretKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  secretKey,
			},
		},
	}
}

func parseNodeIDs(status string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		id := fields[0]
		// Garage node IDs start with 8+ hex chars and are at least 16 chars long
		if len(id) >= 8 && !seen[id] && isHex(id) {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && len(s) > 0
}

func dataSizeGB(q resource.Quantity) int64 {
	gb := q.Value() / (1024 * 1024 * 1024)
	if gb < 1 {
		return 1
	}
	return gb
}

func updateCheckInterval(schedule string) time.Duration {
	if schedule == "" {
		return 24 * time.Hour
	}
	return 24 * time.Hour
}
