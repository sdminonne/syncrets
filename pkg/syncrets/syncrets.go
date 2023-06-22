package syncrets

import (
	"context"
	"encoding/json"
	"k8s.io/client-go/rest"
	"os"
	"path/filepath"
	"sync"

	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	netv1client "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"time"
)

var (
	ArgoNs, CertsNs string
	cluster2Certs   sync.Map
)

type ArgoClusterCredential struct {
	TLSClientConfig `json:"tlsClientConfig"`
}

// TLSClientConfig contains settings to enable transport layer security
type TLSClientConfig struct {
	Insecure   bool   `json:"insecure"`
	ServerName string `json:"serverName,omitempty"`
	CertData   []byte `json:"certData,omitempty"`
	KeyData    []byte `json:"keyData,omitempty"`
	CAData     []byte `json:"caData,omitempty"`
}

// SyncData it's a pair of Secret to sync and Credential for the target cluster
type SyncData struct {
	Host       string                 // Host is the endpoint
	Secret     *corev1.Secret         // Secret it's the secret to sync
	Credential *ArgoClusterCredential // Credential contains the remote cluster credentials from argo cluster secret
}

func NewSyncDataFromSecret(secret *corev1.Secret) SyncData {
	return SyncData{Host: secret.ObjectMeta.Annotations["cert-manager.io/common-name"],
		Secret: secret}
}

func (s *SyncData) UpdateSyncDataWithSecret(secret *corev1.Secret) error {
	s.Secret = secret
	return nil
}

func NewSyncDataFromArgoClusterCredential(argoClusterCredential *ArgoClusterCredential) SyncData {
	return SyncData{Credential: argoClusterCredential}
}

func (s *SyncData) UpdateSyncDataWithArgoClusterCredential(argoClusterCredential *ArgoClusterCredential) error {
	s.Credential = argoClusterCredential
	return nil
}

// check if certificate is expired or invalid
func isCertificateIsExpiredOrInvalid(secretGotten *corev1.Secret) bool {
	pkData := secretGotten.Data[corev1.TLSPrivateKeyKey]
	if len(pkData) == 0 {
		return true
	}
	certData := secretGotten.Data[corev1.TLSCertKey]
	if len(certData) == 0 {
		return true
	}
	_, err := tls.X509KeyPair(certData, pkData)
	if err != nil {
		return true
	}
	block, _ := pem.Decode([]byte(certData))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	if time.Now().After(cert.NotAfter) {
		log.Warnf("certificate in secret %s/%s is expired. It expired on %s",
			secretGotten.GetNamespace(), secretGotten.GetName(), cert.NotAfter)
		return true
	}
	return false

}

// checkSecret creates or update the certificate secret
func checkSecret(ctx context.Context,
	ingress netv1.Ingress,
	coreclient *corev1client.CoreV1Client, secret corev1.Secret) {

	secret.Namespace = ingress.GetNamespace()

	// check if certificate exists
	ss, err := coreclient.Secrets(ingress.GetNamespace()).Get(ctx, secret.GetName(), v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err := coreclient.Secrets(secret.Namespace).Create(context.Background(), &secret, v1.CreateOptions{})
			if err != nil {
				log.Errorf("unable to create secret: %v", err)
				return
			}
			log.Infof("Secret %s/%s created on remote cluster", secret.Namespace, secret.GetName())
			return
		}
		log.Errorf("unable to check whether secret %s/%s exists for remote cluster: %v",
			secret.GetNamespace(), secret.GetName(), err)
		return
	}

	if isCertificateIsExpiredOrInvalid(ss) {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			gottenCertificate, err := coreclient.Secrets(ingress.GetNamespace()).Get(ctx, secret.GetName(), v1.GetOptions{})
			if err != nil {
				return err
			}
			gottenCertificate.Data = secret.Data
			_, err = coreclient.Secrets(secret.Namespace).Update(context.Background(), gottenCertificate, v1.UpdateOptions{})
			return err
		})
		if retryErr != nil {
			log.Errorf("unable to update invalid or expired certificate: %v", retryErr)
		}
	}
	log.Infof("Secret certificate %s/%s OK for ingress: %s/%s",
		secret.GetNamespace(), secret.GetName(), ingress.GetNamespace(), ingress.GetName())
}

func (s *SyncData) Synchronize() error {
	if len(s.Host) == 0 {
		log.Warnf("no host")
		return nil
	}
	if s.Credential == nil {
		log.Warnf("no credential")
		return nil
	}
	if s.Secret == nil {
		log.Info("nothing to synchronize")
		return nil
	}

	restConfig, err := clientcmd.DefaultClientConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("unable to create dfault client config: %v", err)
	}
	restConfig.Host = s.Host
	restConfig.CAData = s.Credential.CAData
	restConfig.CertData = s.Credential.CertData
	restConfig.KeyData = s.Credential.KeyData
	coreclient, err := corev1client.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("unable to init core client from restConfig: %v", err)
	}

	secret := corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:        s.Secret.GetName(),
			Namespace:   "",
			Labels:      s.Secret.GetLabels(),
			Annotations: s.Secret.GetAnnotations(),
		},
		Data: s.Secret.Data,
	}

	netclient, err := netv1client.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("unable to init networking client from restConfig: %v", err)
	}
	var wg sync.WaitGroup
	defer wg.Done()
	ingresses, err := netclient.Ingresses("").List(context.Background(), v1.ListOptions{})
	for _, currentIngress := range ingresses.Items {
		log.Infof("can you see the ingress? %s", currentIngress.GetName())
		for _, tls := range currentIngress.Spec.TLS {
			if tls.SecretName == s.Secret.GetName() {
				wg.Add(1)
				go checkSecret(context.TODO(), currentIngress, coreclient, secret)
			}
		}
	}

	return nil
}

func onAddCertSecret(obj interface{}) {
	log.Info("onAddCertSecret 1")
	secret := obj.(*corev1.Secret)
	if secret == nil {
		log.Warnf("unable to type assert to secret object")
		return
	}

	ipSans := secret.ObjectMeta.Annotations["cert-manager.io/ip-sans"]
	cn := secret.ObjectMeta.Annotations["cert-manager.io/common-name"]

	log.Infof("onAddCertSecret IP: %s", ipSans)
	log.Infof("onAddCertSecret CN: %s", cn)
	if len(cn) == 0 {
		log.Warnf("No common name found in secret %s/%s", secret.GetNamespace(), secret.GetName())
		return
	}
	result, ok := cluster2Certs.Load(cn)
	var sd SyncData
	switch ok {
	case true:
		sd = result.(SyncData)
		if err := sd.UpdateSyncDataWithSecret(secret); err != nil {
			log.Warnf("Couldn't update certificate secret for cluster %s: %v", cn, err)
			return
		}
		log.Infof("Updated certificate secret for cluster %s", cn)
	case false:
		log.Infof("Storing info for cluster %s", cn)
		cluster2Certs.Store(cn, NewSyncDataFromSecret(secret))
	}

	if err := sd.Synchronize(); err != nil {
		log.Errorf("onAddCertSecret: couldn't synchronize: %v", err)
		return
	}
	log.Infof("Secret synchronized")
	return
}

func onAddArgoClusterSecret(obj interface{}) {
	log.Info("onAddArgoClusterSecret")
	secret := obj.(*corev1.Secret)
	if secret == nil {
		log.Warnf("unable to type assert to secret object")
		return
	}

	server := string(secret.Data["server"])
	var argoCreds ArgoClusterCredential
	if err := json.Unmarshal(secret.Data["config"], &argoCreds); err != nil {
		log.Errorf("unable to unmarshal secret to cluster with server %s: %v", server, err)
		return
	}

	var sd SyncData
	result, ok := cluster2Certs.Load(string(secret.Data["name"]))
	if ok {
		sd = result.(SyncData)
		if err := sd.UpdateSyncDataWithArgoClusterCredential(&argoCreds); err != nil {
			log.Warnf("Couldn't update Argo credential for %s: %v", secret.Data["name"], err)
			return
		}
		sd.Host = server
		log.Infof("updating Argo credential for cluster %s", secret.Data["name"])
	} else {
		sd := NewSyncDataFromArgoClusterCredential(&argoCreds)
		sd.Host = server
		cluster2Certs.Store(string(secret.Data["name"]), sd)
	}

	cluster2Certs.Store(string(secret.Data["name"]), sd)
	if err := sd.Synchronize(); err != nil {
		log.Errorf("onAddArgoClusterSecret: couldn't synchronize: %v", err)
		return
	}
	log.Infof("Secret synchronized")
	return
}

func DoTheJob() {
	config, err := rest.InClusterConfig()
	if err != nil {
		// maybe we're not in cluster
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatal(err)
		}
		log.Infof("Not in cluster, using %s", kubeconfig)
	}
	// TODO add WorkQueue, add CrashHandler, add leaderEleection
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	argoClusterSecretsOnly := informers.WithTweakListOptions(func(opts *v1.ListOptions) {
		opts.LabelSelector = "argocd.argoproj.io/secret-type=cluster"
	})
	argoClusterSecretFactory := informers.NewSharedInformerFactoryWithOptions(clientset, 5*time.Second,
		informers.WithNamespace(ArgoNs), argoClusterSecretsOnly)

	argoClusterSecretsInformer := argoClusterSecretFactory.Core().V1().Secrets().Informer()

	certManagerSecretsOnly := informers.WithTweakListOptions(func(opts *v1.ListOptions) {
		opts.LabelSelector = "controller.cert-manager.io/fao=true"
	})
	certManagerSecretsSecretFactory := informers.NewSharedInformerFactoryWithOptions(clientset, 5*time.Second,
		informers.WithNamespace(CertsNs), certManagerSecretsOnly)

	certManagerSecretsInformer := certManagerSecretsSecretFactory.Core().V1().Secrets().Informer()

	stopper := make(chan struct{})

	defer close(stopper)
	defer runtime.HandleCrash()

	argoClusterSecretsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: onAddArgoClusterSecret,
	})
	log.Infof("Watching for argoCD cluster secrets namespace: %s", ArgoNs)
	go argoClusterSecretsInformer.Run(stopper)

	certManagerSecretsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: onAddCertSecret,
	})
	log.Infof("Watching for cert-manager cluster secrets namespace: %s", CertsNs)
	go certManagerSecretsInformer.Run(stopper)

	<-stopper
}
