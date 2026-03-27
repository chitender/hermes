// Package controller implements the VaultEtcdSync reconciler.
package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"k8s.io/client-go/util/workqueue"

	syncv1alpha1 "github.com/example/vault-etcd-sync-operator/api/v1alpha1"
	etcdclient "github.com/example/vault-etcd-sync-operator/internal/etcd"
	tmplrenderer "github.com/example/vault-etcd-sync-operator/internal/template"
	vaultclient "github.com/example/vault-etcd-sync-operator/internal/vault"
)

const (
	finalizerName = "sync.example.com/vault-etcd-watcher"

	// Condition types
	conditionReady = "Ready"

	// Condition reasons
	reasonSyncSuccess     = "SyncSuccess"
	reasonSyncFailed      = "SyncFailed"
	reasonVaultUnreachable = "VaultUnreachable"
	reasonEtcdUnreachable  = "EtcdUnreachable"

	// Error label values for the errors_total metric
	errTypeVaultRead      = "vault_read"
	errTypeTemplateRender = "template_render"
	errTypeEtcdWrite      = "etcd_write"
)

// ─── Prometheus metrics ───────────────────────────────────────────────────────

var (
	metricLastSyncTimestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vault_etcd_sync_last_sync_timestamp_seconds",
		Help: "Unix timestamp of the last successful sync per CR.",
	}, []string{"namespace", "name"})

	metricErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vault_etcd_sync_errors_total",
		Help: "Total number of sync errors per CR and error type.",
	}, []string{"namespace", "name", "error_type"})

	metricSecretVersion = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vault_etcd_sync_secret_version",
		Help: "Current Vault KV v2 version number per path.",
	}, []string{"namespace", "name", "vault_path"})
)

// RegisterMetrics registers Prometheus metrics with the given registerer.
// Call this once from main before starting the manager.
func RegisterMetrics(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		metricLastSyncTimestamp,
		metricErrorsTotal,
		metricSecretVersion,
	} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// ─── Reconciler ───────────────────────────────────────────────────────────────

// VaultEtcdSyncReconciler reconciles VaultEtcdSync objects.
//
// +kubebuilder:rbac:groups=sync.example.com,resources=vaultedcsyncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sync.example.com,resources=vaultedcsyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sync.example.com,resources=vaultedcsyncs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
type VaultEtcdSyncReconciler struct {
	client.Client
	Log      logr.Logger
	Recorder record.EventRecorder

	// watcherCh receives events from Vault watcher goroutines to trigger
	// an out-of-band reconcile (Vault secret version changed).
	watcherCh chan event.GenericEvent

	// watchers tracks running Vault watcher goroutines, keyed by
	// NamespacedName.String(). Values are context.CancelFunc.
	watchers vaultclient.WatcherRegistry

	// rootCtx is the manager's root context. Watcher goroutines are parented
	// to this context so they live beyond the per-reconcile request context.
	rootCtx context.Context //nolint:containedctx
}

// SetupWithManager registers the controller with the Manager and configures
// all watches, including the channel used by Vault watcher goroutines.
func (r *VaultEtcdSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.watcherCh = make(chan event.GenericEvent, 256)
	r.rootCtx = mgr.GetContext() // manager context lives as long as the process

	return ctrl.NewControllerManagedBy(mgr).
		For(&syncv1alpha1.VaultEtcdSync{}).
		// Watch the channel that Vault watchers write to when a secret changes.
		// source.Channel is generic in controller-runtime v0.19+; we use
		// handler.TypedFuncs with the concrete workqueue type.
		WatchesRawSource(
			source.Channel(r.watcherCh, handler.TypedFuncs[event.GenericEvent, reconcile.Request]{
				GenericFunc: func(
					ctx context.Context,
					e event.GenericEvent,
					q workqueue.TypedRateLimitingInterface[reconcile.Request],
				) {
					q.Add(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(e.Object)})
				},
			}),
		).
		Complete(r)
}

// Reconcile implements reconcile.Reconciler.
//
// Lifecycle:
//  1. CR deletion → stop watcher goroutine, remove finalizer.
//  2. CR create/update → auth, read all Vault paths, render template,
//     validate JSON, write to etcd, update status, start/restart watcher.
func (r *VaultEtcdSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cr", req.NamespacedName)

	// ── Fetch the CR ─────────────────────────────────────────────────────────
	var ves syncv1alpha1.VaultEtcdSync
	if err := r.Get(ctx, req.NamespacedName, &ves); err != nil {
		if apierrors.IsNotFound(err) {
			// CR was deleted; the finalizer handler below already cleaned up.
			r.watchers.Stop(req.NamespacedName.String())
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get VaultEtcdSync: %w", err)
	}

	// ── Deletion handling ─────────────────────────────────────────────────────
	if !ves.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, logger, &ves, req.NamespacedName)
	}

	// ── Ensure finalizer is present ───────────────────────────────────────────
	if !controllerutil.ContainsFinalizer(&ves, finalizerName) {
		controllerutil.AddFinalizer(&ves, finalizerName)
		if err := r.Update(ctx, &ves); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		// The update will re-trigger Reconcile; return here.
		return ctrl.Result{}, nil
	}

	// ── Parse sync interval ───────────────────────────────────────────────────
	interval, err := vaultclient.ParseSyncInterval(ves.Spec.SyncInterval)
	if err != nil {
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonSyncFailed, err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	logger.Info("reconciling", "etcdKey", ves.Spec.EtcdKey, "interval", interval)

	// ── Read Vault auth secret ────────────────────────────────────────────────
	vaultSecretData, err := r.readSecret(ctx, ves.Spec.VaultRef.SecretRef)
	if err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeVaultRead, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonVaultUnreachable,
			fmt.Sprintf("cannot read vault auth secret: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// ── Build Vault client ────────────────────────────────────────────────────
	vaultClient, err := vaultclient.NewClient(ctx, vaultSecretData, logger)
	if err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeVaultRead, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonVaultUnreachable,
			fmt.Sprintf("vault auth failed: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// ── Read all Vault paths ──────────────────────────────────────────────────
	pathData, err := r.readAllVaultPaths(ctx, logger, vaultClient, &ves)
	if err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeVaultRead, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonVaultUnreachable, err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// ── Merge and render template ─────────────────────────────────────────────
	merged := tmplrenderer.MergeVaultData(pathData)
	rendered, err := tmplrenderer.Render(ves.Spec.JSONTemplate, merged)
	if err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeTemplateRender, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonSyncFailed,
			fmt.Sprintf("template render failed: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// ── Build etcd client ─────────────────────────────────────────────────────
	etcdAuthData, err := r.readSecret(ctx, ves.Spec.EtcdRef.SecretRef)
	if err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeEtcdWrite, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonEtcdUnreachable,
			fmt.Sprintf("cannot read etcd auth secret: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	var etcdTLSData map[string][]byte
	if ves.Spec.EtcdRef.TLSSecretRef != "" {
		etcdTLSData, err = r.readSecret(ctx, ves.Spec.EtcdRef.TLSSecretRef)
		if err != nil {
			r.recordErrorEvent(ctx, logger, &ves, errTypeEtcdWrite, err)
			r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonEtcdUnreachable,
				fmt.Sprintf("cannot read etcd TLS secret: %v", err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	etcdCfg, err := etcdclient.ParseConfig(ves.Spec.EtcdRef.Endpoints, etcdAuthData, etcdTLSData)
	if err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeEtcdWrite, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonEtcdUnreachable, err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	ec, err := etcdclient.NewClient(etcdCfg, logger)
	if err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeEtcdWrite, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonEtcdUnreachable,
			fmt.Sprintf("etcd connect failed: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	defer ec.Close()

	// ── Write to etcd ─────────────────────────────────────────────────────────
	if err := ec.Put(ctx, ves.Spec.EtcdKey, rendered); err != nil {
		r.recordErrorEvent(ctx, logger, &ves, errTypeEtcdWrite, err)
		r.setCondition(ctx, logger, &ves, metav1.ConditionFalse, reasonEtcdUnreachable,
			fmt.Sprintf("etcd write failed: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// ── Success: update status & metrics ──────────────────────────────────────
	now := time.Now()
	ves.Status.LastSyncTime = now.UTC().Format(time.RFC3339)
	ves.Status.LastSyncStatus = "Success"
	r.setConditionOnObject(&ves, metav1.ConditionTrue, reasonSyncSuccess,
		fmt.Sprintf("synced %d vault paths to etcd key %s", len(ves.Spec.VaultPaths), ves.Spec.EtcdKey))

	if err := r.Status().Update(ctx, &ves); err != nil {
		logger.Error(err, "failed to update status after successful sync")
	}

	metricLastSyncTimestamp.WithLabelValues(ves.Namespace, ves.Name).Set(float64(now.Unix()))
	r.Recorder.Eventf(&ves, corev1.EventTypeNormal, reasonSyncSuccess,
		"Synced %d vault paths to etcd key %s", len(ves.Spec.VaultPaths), ves.Spec.EtcdKey)

	logger.Info("sync successful", "etcdKey", ves.Spec.EtcdKey)

	// ── Start / restart Vault watcher ─────────────────────────────────────────
	// Use the manager's root context (not the reconcile ctx) so the watcher
	// goroutine outlives this single reconcile invocation.
	r.startWatcher(r.rootCtx, logger, vaultClient, &ves, req.NamespacedName, interval)

	return ctrl.Result{RequeueAfter: interval}, nil
}

// ─── Deletion ─────────────────────────────────────────────────────────────────

func (r *VaultEtcdSyncReconciler) handleDeletion(
	ctx context.Context,
	logger logr.Logger,
	ves *syncv1alpha1.VaultEtcdSync,
	nn types.NamespacedName,
) (ctrl.Result, error) {
	logger.Info("handling deletion, stopping watcher")
	r.watchers.Stop(nn.String())

	if controllerutil.ContainsFinalizer(ves, finalizerName) {
		controllerutil.RemoveFinalizer(ves, finalizerName)
		if err := r.Update(ctx, ves); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
	}
	return ctrl.Result{}, nil
}

// ─── Vault path reading ───────────────────────────────────────────────────────

func (r *VaultEtcdSyncReconciler) readAllVaultPaths(
	ctx context.Context,
	logger logr.Logger,
	vc *vaultclient.Client,
	ves *syncv1alpha1.VaultEtcdSync,
) ([]tmplrenderer.PathData, error) {
	pathData := make([]tmplrenderer.PathData, 0, len(ves.Spec.VaultPaths))

	for _, vp := range ves.Spec.VaultPaths {
		// Read secret data — never log values, only key names.
		data, err := vc.GetSecretKV(ctx, vp.Path)
		if err != nil {
			return nil, fmt.Errorf("vault read %q: %w", vp.Path, err)
		}
		logger.V(1).Info("vault path read", "path", vp.Path, "keys", secretKeys(data))

		// Record current version in metrics (fire-and-forget; errors non-fatal).
		if sv, svErr := vc.GetSecretVersion(ctx, vp.Path); svErr == nil {
			metricSecretVersion.WithLabelValues(ves.Namespace, ves.Name, vp.Path).
				Set(float64(sv.Version))
		}

		pathData = append(pathData, tmplrenderer.PathData{
			VaultPath: vp,
			Prefix:    vp.Prefix,
			Data:      data,
		})
	}

	return pathData, nil
}

// secretKeys returns only the keys from a secret data map, for safe logging.
func secretKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ─── Watcher lifecycle ────────────────────────────────────────────────────────

func (r *VaultEtcdSyncReconciler) startWatcher(
	ctx context.Context,
	logger logr.Logger,
	vc *vaultclient.Client,
	ves *syncv1alpha1.VaultEtcdSync,
	nn types.NamespacedName,
	interval time.Duration,
) {
	paths := make([]string, len(ves.Spec.VaultPaths))
	for i, vp := range ves.Spec.VaultPaths {
		paths[i] = vp.Path
	}

	// Create a shallow copy of the CR object to use as the GenericEvent carrier.
	// We only need Name and Namespace; the watcher must not hold a mutable reference.
	vesRef := &syncv1alpha1.VaultEtcdSync{}
	vesRef.Name = ves.Name
	vesRef.Namespace = ves.Namespace

	cb := func() {
		logger.Info("vault watcher detected secret change, enqueuing reconcile")
		select {
		case r.watcherCh <- event.GenericEvent{Object: vesRef}:
		default:
			// Channel full — the reconcile is already queued; drop this signal.
			logger.V(1).Info("watcher channel full, dropping duplicate event")
		}
	}

	watcher := vaultclient.NewWatcher(vc, paths, interval, cb, logger)
	// StartOrReplace uses a child context derived from the manager's root ctx,
	// so the goroutine is also cancelled when the manager shuts down.
	r.watchers.StartOrReplace(nn.String(), watcher, ctx)
}

// ─── Status helpers ───────────────────────────────────────────────────────────

func (r *VaultEtcdSyncReconciler) setCondition(
	ctx context.Context,
	logger logr.Logger,
	ves *syncv1alpha1.VaultEtcdSync,
	status metav1.ConditionStatus,
	reason, message string,
) {
	r.setConditionOnObject(ves, status, reason, message)
	if err := r.Status().Update(ctx, ves); err != nil {
		logger.Error(err, "failed to update status conditions")
	}
}

func (r *VaultEtcdSyncReconciler) setConditionOnObject(
	ves *syncv1alpha1.VaultEtcdSync,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()
	cond := metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: ves.Generation,
	}

	// Update existing condition or append a new one.
	for i, c := range ves.Status.Conditions {
		if c.Type == conditionReady {
			if c.Status != status {
				ves.Status.Conditions[i] = cond
			} else {
				// Status unchanged; only update message / observed generation.
				ves.Status.Conditions[i].Message = message
				ves.Status.Conditions[i].ObservedGeneration = ves.Generation
			}
			return
		}
	}
	ves.Status.Conditions = append(ves.Status.Conditions, cond)
}

func (r *VaultEtcdSyncReconciler) recordErrorEvent(
	ctx context.Context,
	logger logr.Logger,
	ves *syncv1alpha1.VaultEtcdSync,
	errType string,
	err error,
) {
	metricErrorsTotal.WithLabelValues(ves.Namespace, ves.Name, errType).Inc()
	r.Recorder.Eventf(ves, corev1.EventTypeWarning, reasonSyncFailed,
		"sync error (%s): %v", errType, sanitiseError(err))
	logger.Error(err, "sync error", "errorType", errType)
}

// sanitiseError removes any potential Vault token strings from error messages.
// Vault tokens start with "s." or "hvs." followed by base64 characters.
func sanitiseError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// Simple heuristic: replace anything that looks like a Vault token.
	for _, prefix := range []string{"s.", "hvs."} {
		if idx := strings.Index(msg, prefix); idx >= 0 {
			msg = msg[:idx] + "[VAULT_TOKEN_REDACTED]"
			break
		}
	}
	return fmt.Errorf("%s", msg) //nolint:err113
}

// ─── Secret reading helper ────────────────────────────────────────────────────

// readSecret fetches a K8s Secret by "namespace/name" reference and returns
// its raw Data map.
func (r *VaultEtcdSyncReconciler) readSecret(
	ctx context.Context,
	ref string,
) (map[string][]byte, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid secret ref %q: expected namespace/name", ref)
	}
	ns, name := parts[0], parts[1]

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &secret); err != nil {
		return nil, fmt.Errorf("reading secret %q: %w", ref, err)
	}

	return secret.Data, nil
}
