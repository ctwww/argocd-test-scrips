package main

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient"
	applicationpkg "github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	sessionpkg "github.com/argoproj/argo-cd/v2/pkg/apiclient/session"
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/sirupsen/logrus"
	"k8s.io/utils/ptr"
)

// argo-cd info
var serverAddr = ""
var userName = ""
var password = ""

// fetch app info
var appName = "appName"
var ns = "ns"
var kind = "Pod"

var concurrency = 5

func main() {
	for {
		wg := sync.WaitGroup{}
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				logrus.Infof("send request")
				_, err := DefaultArgoServerClient.Get().GetManagedResources(context.TODO(), appName, WithNamespace(ns), WithKind(kind))
				if err != nil {
					logrus.Errorf(err.Error())
				}
				wg.Done()
			}()
		}
		wg.Wait()

		time.Sleep(10 * time.Second)
	}

}

type ArgoServerClient struct {
	mu     *sync.RWMutex
	log    *logrus.Logger
	stopCh chan struct{}

	appClient applicationpkg.ApplicationServiceClient
	closers   []io.Closer
}

var DefaultArgoServerClient = NewSingleton(func() *ArgoServerClient {
	client := NewArgoServerClientOrDie()
	return client
})

func NewArgoServerClientOrDie() *ArgoServerClient {
	opt := &apiclient.ClientOptions{
		ServerAddr: serverAddr,
		PlainText:  false,
		Insecure:   true,
		GRPCWeb:    true,
	}
	apiClient, err := apiclient.NewClient(opt)
	if err != nil {
		logrus.Fatal("failed to create argocd apiclient", err)
		return nil
	}
	sessConn, sessIf := apiClient.NewSessionClientOrDie()
	defer sessConn.Close()
	createdSession, err := sessIf.Create(context.Background(), &sessionpkg.SessionCreateRequest{
		Username: userName,
		Password: password,
	})
	if err != nil {
		logrus.Fatal("failed to create argocd apiclient", err)
		return nil
	}
	opt.AuthToken = createdSession.Token
	apiClient, err = apiclient.NewClient(opt)
	if err != nil {
		logrus.Fatal("failed to create argocd apiclient", err)
		return nil
	}

	closers := make([]io.Closer, 0)
	appClientCloser, appClient := apiClient.NewApplicationClientOrDie()
	closers = append(closers, appClientCloser)
	return &ArgoServerClient{
		mu:     &sync.RWMutex{},
		log:    &logrus.Logger{},
		stopCh: make(chan struct{}),

		closers:   closers,
		appClient: appClient,
	}
}

const (
	DefaultAppProject   = "model-instance"
	DefaultAppNamespace = "argocd"
)

type ResourceQueryOpts func(*applicationpkg.ResourcesQuery)

func WithNamespace(namespace string) ResourceQueryOpts {
	return func(query *applicationpkg.ResourcesQuery) {
		if namespace != "" {
			query.Namespace = &namespace
		}
	}
}
func WithKind(kind string) ResourceQueryOpts {
	return func(query *applicationpkg.ResourcesQuery) {
		if kind != "" {
			query.Kind = &kind
		}
	}
}

func (a *ArgoServerClient) GetManagedResources(
	ctx context.Context,
	argoAppName string,
	opts ...ResourceQueryOpts,
) (*applicationpkg.ManagedResourcesResponse, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	query := &applicationpkg.ResourcesQuery{
		ApplicationName: Ref(argoAppName),
		Project:         Ref(DefaultAppProject),
	}
	for _, opt := range opts {
		opt(query)
	}

	resources, err := a.appClient.ManagedResources(ctx, query)
	if err != nil {
		return nil, err
	}

	// If the query is for a non-direct resource, and the resources are empty, it means the resource is not directly
	// rendered from the app, but a generated resource like a Pod.
	// Group is allowed to be nil for K8s default group
	queryNonDirectResource := len(resources.Items) == 0 && query.Name != nil && query.Namespace != nil && query.Kind != nil
	if queryNonDirectResource {
		req := &applicationpkg.ApplicationResourceRequest{
			Name:         Ref(argoAppName),
			Project:      Ref(DefaultAppProject),
			AppNamespace: Ref(DefaultAppNamespace),
			Namespace:    query.Namespace,
			ResourceName: query.Name,
			Version:      query.Version,
			Kind:         query.Kind,
			Group:        query.Group,
		}
		var resource *applicationpkg.ApplicationResourceResponse
		resource, err = a.appClient.GetResource(ctx, req)
		if err != nil {
			return nil, err
		}

		// Let's unify the response format
		resources.Items = append(resources.Items, &v1alpha1.ResourceDiff{
			Group:       ptr.Deref(query.Group, ""),
			Kind:        *query.Kind,
			Namespace:   *query.Namespace,
			Name:        *query.Name,
			TargetState: "",
			LiveState:   ptr.Deref(resource.Manifest, ""),
		})
	}

	return resources, nil
}

// Singleton global unique data struct
type Singleton[T any] struct {
	once sync.Once

	loader func() T
	data   T
}

// Get retrieve underlying data, if not initialized, will trigger initialization
func (in *Singleton[T]) Get() T {
	if in.loader != nil {
		in.once.Do(func() {
			in.data = in.loader()
		})
	}
	return in.data
}

// Set write the underlying data
func (in *Singleton[T]) Set(data *T) {
	in.once.Do(func() {})
	in.data = *data
}

// Reload trigger loader
func (in *Singleton[T]) Reload() {
	if in.loader != nil {
		dataT := in.loader()
		in.Set(&dataT)
	}
}

// NewSingleton create a new singleton with loader
func NewSingleton[T any](loader func() T) *Singleton[T] {
	return &Singleton[T]{
		loader: loader,
	}
}

func Ref[T any](i T) *T {
	return &i
}
