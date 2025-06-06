package controllers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/bakito/vault-unsealer/pkg/cache"
	"github.com/bakito/vault-unsealer/pkg/constants"
	"github.com/hashicorp/vault-client-go"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ExternalHandler handles external vaults.
type ExternalHandler struct {
	client.Client
	Scheme     *runtime.Scheme
	startedMux sync.Mutex
	started    bool
	secrets    []corev1.Secret
	Cache      cache.Cache
}

// SetupWithManager sets up the controller with the Manager.
func (r *ExternalHandler) SetupWithManager(mgr ctrl.Manager, secretsExternal []corev1.Secret) error {
	r.secrets = secretsExternal
	return mgr.Add(r)
}

func (r *ExternalHandler) Start(ctx context.Context) error {
	r.startedMux.Lock()
	if r.started {
		return errors.New("handler is already running")
	}
	r.started = true
	r.startedMux.Unlock()

	grp, ctx := errgroup.WithContext(ctx)

	for _, s := range r.secrets {
		grp.Go(func() error {
			err := r.setupVaultCheckLoop(ctx, s)
			if err != nil {
				return err
			}
			return context.Canceled
		})
	}

	_ = grp.Wait()
	return nil
}

func (r *ExternalHandler) setupVaultCheckLoop(ctx context.Context, secret corev1.Secret) error {
	if r.Cache.VaultInfoFor(secret.Name) == nil {
		v := extractVaultInfo(secret)
		r.Cache.SetVaultInfoFor(secret.Name, v)
	}

	duration := r.getInterval(ctx, secret)

	srcCl, err := r.getSourceClient(secret)
	if err != nil {
		return err
	}

	trgtsCl, err := r.getTargetClients(secret)
	if err != nil {
		return err
	}

	// initial handle cycle
	r.handleExternal(ctx, secret.Name, srcCl, trgtsCl)

	// start the loop
	t := time.NewTicker(duration).C
	for {
		select {
		case <-t:
			r.handleExternal(ctx, secret.Name, srcCl, trgtsCl)
		case <-ctx.Done():
			return nil
		}
	}
}

func (r *ExternalHandler) handleExternal(
	ctx context.Context,
	name string,
	srcCl *vault.Client,
	trgtCl []*vault.Client,
) {
	l := log.FromContext(ctx).WithValues("secret", name)

	vi := r.Cache.VaultInfoFor(name)
	if vi == nil || len(vi.UnsealKeys) == 0 {
		l.Info("no unseal info found, starting lookup")

		err := login(ctx, srcCl, vi)
		if err != nil {
			l.Error(err, "login error")
			return
		}

		if err = readUnsealKeys(ctx, srcCl, vi); err != nil {
			l.Error(err, "error reading unseal keys")
			return
		}

		r.Cache.SetVaultInfoFor(name, vi)
		l.WithValues("keys", len(vi.UnsealKeys)).Info("successfully read unseal keys from vault")
	}

	for _, cl := range trgtCl {
		l.Info("checking seal status")

		st, err := cl.System.SealStatus(ctx)
		if err != nil {
			l.Error(err, "error checking seal status")
			continue
		}

		if !st.Data.Initialized {
			l.Info("vault is not initialized")
			continue
		}

		if st.Data.Sealed {
			l.Info("vault is sealed, starting unseal")
			if err := unseal(ctx, cl, vi); err != nil {
				l.Error(err, "error unsealing vault")
			} else {
				l.Info("successfully unsealed vault")
			}
		}
	}
}

func (r *ExternalHandler) getInterval(ctx context.Context, secret corev1.Secret) time.Duration {
	str := secret.Labels[constants.LabelExternal]
	duration, err := time.ParseDuration(str)
	if err != nil {
		log.FromContext(ctx).WithValues("secret", secret.Name).
			Error(err, "error parsing duration, fallback to default", "duration", str, "default", constants.DefaultExternalInterval)
		duration = constants.DefaultExternalInterval
	}
	return duration
}

func (r *ExternalHandler) getSourceClient(secret corev1.Secret) (*vault.Client, error) {
	src, ok := secret.Annotations[constants.AnnotationExternalSource]
	if !ok {
		return nil, errors.New("no source found")
	}

	return newClient(src, false)
}

func (r *ExternalHandler) getTargetClients(secret corev1.Secret) ([]*vault.Client, error) {
	trgt, ok := secret.Annotations[constants.AnnotationExternalTargets]
	if !ok {
		return nil, errors.New("no targets found")
	}

	trgts := strings.Split(trgt, ";")
	var trgtsCl []*vault.Client

	for _, t := range trgts {
		tcl, err := newClient(t, false)
		if err != nil {
			return nil, err
		}
		trgtsCl = append(trgtsCl, tcl)
	}

	return trgtsCl, nil
}
