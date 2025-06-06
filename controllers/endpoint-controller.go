package controllers

import (
	"context"

	"github.com/bakito/vault-unsealer/pkg/cache"
	"github.com/bakito/vault-unsealer/pkg/hierarchy"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// EndpointsReconciler reconciles an Endpoints object.
type EndpointsReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Cache            cache.Cache
	UnsealerSelector labels.Selector
}

//+kubebuilder:rbac:groups=,resources=endpoints,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments;replicasets,verbs=get;list;watch

// Reconcile reconciles the Endpoints object.
func (r *EndpointsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var ep client.Object
	if r.Cache.IsK8sPast123() {
		ep = &discoveryv1.EndpointSlice{}
	} else {
		ep = &corev1.Endpoints{} //nolint:staticcheck // deprecation is handled
	}

	err := r.Get(ctx, req.NamespacedName, ep)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the req.
		l.Error(err, "Error reading endpoints")
		return reconcile.Result{}, err
	}

	// Update the cache members if needed and sync the cache.
	if r.Cache.SetMember(hierarchy.GetPeersFrom(ep)) {
		r.Cache.Sync()
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EndpointsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	var ep client.Object
	if r.Cache.IsK8sPast123() {
		ep = &discoveryv1.EndpointSlice{}
	} else {
		ep = &corev1.Endpoints{} //nolint:staticcheck // deprecation is handled
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(ep).
		WithEventFilter(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return r.UnsealerSelector.Matches(labels.Set(e.Object.GetLabels()))
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return r.UnsealerSelector.Matches(labels.Set(e.ObjectNew.GetLabels()))
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return r.UnsealerSelector.Matches(labels.Set(e.Object.GetLabels()))
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return r.UnsealerSelector.Matches(labels.Set(e.Object.GetLabels()))
			},
		}).
		Complete(r)
}
