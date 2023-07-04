package syncrets

// https://github.com/kubernetes/sample-controller/blob/master/controller.go#L654
// https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/job/job_controller.go
// https://github.com/kubernetes/client-go/blob/master/informers/core/v1/secret.go
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	secretsinfoermers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	clientset "k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"

	log "github.com/sirupsen/logrus"
)

type Controller struct {
	clientset clientset.Interface

	syncHandler func(ctx context.Context, jobKey string) error

	argoClusterSecretLister corev1listers.SecretLister
	argoClusterSecretSynced cache.InformerSynced

	certSecretLister corev1listers.SecretLister
	certSecretSynced cache.InformerSynced

	// secrets to be updated
	queue workqueue.RateLimitingInterface
}

func NewController(clientset clientset.Interface,
	argoClusterSecretInfomer secretsinfoermers.SecretInformer,
	certSecretInformer secretsinfoermers.SecretInformer) *Controller {
	c := &Controller{
		clientset:               clientset,
		argoClusterSecretLister: argoClusterSecretInfomer.Lister(),
		argoClusterSecretSynced: argoClusterSecretInfomer.Informer().HasSynced,
		certSecretLister:        certSecretInformer.Lister(),
		certSecretSynced:        certSecretInformer.Informer().HasSynced,
		queue:                   workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
	}
	c.syncHandler = c.syncSecret

	argoClusterSecretInfomer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: c.syncArgoClusterSecret,
			UpdateFunc: func(old, new interface{}) {
				log.Infof("Update argoClusterSecretInfomer...")
			},
			DeleteFunc: c.syncArgoClusterSecret,
		}, 10*time.Minute)

	certSecretInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc: c.syncCertSecret,
		UpdateFunc: func(old, new interface{}) {
			log.Infof("Update certSecretInformer...")
		},
		DeleteFunc: c.syncCertSecret,
	}, 12*time.Hour)

	return c
}

func (c *Controller) enqueueSecret(obj interface{}) {
	var (
		key string
		err error
	)
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.queue.Add(key)
}
func (c *Controller) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	log.Infof("Starting syncrets controller")
	defer log.Info("Shutting down syncrets controller")

	if !cache.WaitForNamedCacheSync("syncrets", ctx.Done(), c.certSecretSynced, c.argoClusterSecretSynced) {
		log.Error("Can't wait for named cache sync")
		return
	}

	log.Infof("Starting %d workers", workers)
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.worker, time.Second)
	}

	<-ctx.Done()
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (c *Controller) worker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	obj, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	err := func(obj interface{}) error {
		defer c.queue.Done(obj)
		var (
			key string
			ok  bool
		)
		if key, ok = obj.(string); !ok {
			c.queue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected secret in queue but got %#v", obj))
			return nil
		}
		if err := c.syncHandler(ctx, key); err != nil {
			c.queue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		c.queue.Forget(obj)
		log.Info("Successfully synced", "resourceName", key)
		return nil
	}(obj)
	if err != nil {
		utilruntime.HandleError(err)
	}
	return true
}

func Run() {
	stopper := make(chan struct{})

	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatal(err)
		}
		log.Infof("Not in cluster, using %s", kubeconfig)
	}
	// TODO add leaderEleection
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}
	argoClusterSecretsOnly := informers.WithTweakListOptions(func(opts *v1.ListOptions) {
		opts.LabelSelector = "argocd.argoproj.io/secret-type=cluster"
	})
	argoClusterSecretFactory := informers.NewSharedInformerFactoryWithOptions(clientset, 5*time.Second,
		informers.WithNamespace(ArgoNs), argoClusterSecretsOnly)

	certManagerSecretsOnly := informers.WithTweakListOptions(func(opts *v1.ListOptions) {
		opts.LabelSelector = "controller.cert-manager.io/fao=true"
	})
	certManagerSecretsSecretFactory := informers.NewSharedInformerFactoryWithOptions(clientset, 5*time.Second,
		informers.WithNamespace(CertsNs), certManagerSecretsOnly)

	controller := NewController(clientset,
		argoClusterSecretFactory.Core().V1().Secrets(),
		certManagerSecretsSecretFactory.Core().V1().Secrets())

	argoClusterSecretFactory.Start(stopper)

	certManagerSecretsSecretFactory.Start(stopper)

	controller.Run(context.TODO(), 1)
	<-stopper
}
