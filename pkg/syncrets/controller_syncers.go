package syncrets

import (
	"context"
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	netv1client "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

func updateRemoteSecret(scanRemoteIngress bool, server string, credential ArgoClusterCredential,
	secret *corev1.Secret) error {
	restConfig, err := clientcmd.DefaultClientConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("unable to create dfault client config: %v", err)
	}
	restConfig.Host = server
	restConfig.CAData = credential.CAData
	restConfig.CertData = credential.CertData
	restConfig.KeyData = credential.KeyData
	coreclient, err := corev1client.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("unable to init core client from restConfig: %v", err)
	}

	remoteSecret := corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:        secret.GetName(),
			Namespace:   "",
			Labels:      secret.GetLabels(),
			Annotations: secret.GetAnnotations(),
		},
		Data: secret.Data,
	}
	if scanRemoteIngress {
		netclient, err := netv1client.NewForConfig(restConfig)
		if err != nil {
			return fmt.Errorf("unable to init networking client from restConfig: %v", err)
		}
		ingresses, err := netclient.Ingresses("").List(context.Background(), v1.ListOptions{})
		for _, currentIngress := range ingresses.Items {
			for _, tls := range currentIngress.Spec.TLS {
				if tls.SecretName == secret.GetName() {
					checkSecret(context.TODO(), currentIngress, coreclient, remoteSecret)
				}
			}
		}
	}

	if err != nil {
		return fmt.Errorf("unable to init networking client from restConfig: %v", err)
	}

	return nil
}

func (c *Controller) syncSecret(ctx context.Context, key string) error {
	log.Info("syncSecret BEGIN")
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if len(ns) == 0 || len(name) == 0 {
		return fmt.Errorf("invalid key %q: either namespace or name is missing", key)
	}
	log.Infof("syncSecret %s/%s", ns, name)
	return nil
}

func (c *Controller) syncArgoClusterSecret(obj interface{}) {
	log.Info("syncArgoClusterSecret...")
	secret := obj.(*corev1.Secret)
	if secret == nil {
		log.Warnf("unable to type assert to secret object")
		return
	}
	server := string(secret.Data["server"])
	log.Infof("Got secret with server: %s", server)
	name := string(secret.Data["name"])
	if len(name) == 0 {
		log.Warnf("no name definedd in secret. This looks like the 'in-cluster' ArgoCD secret. Skipping it.")
		return
	}
	log.Infof("Got secret with name: %s", name)
	var argoCreds ArgoClusterCredential
	if err := json.Unmarshal(secret.Data["config"], &argoCreds); err != nil {
		log.Errorf("unable to unmarshal secret to cluster with server %s: %v", server, err)
		return
	}

	log.Info("Now we should get the cert secret from the lister")
	certsSecrets, err := c.certSecretLister.Secrets(CertsNs).List(labels.Everything()) // TODO add label with CN to get by label selector
	if err != nil {
		log.Errorf("unable to list secret in namespace %s: %v", CertsNs, err)
		return
	}
	for _, secret := range certsSecrets {
		cn := secret.ObjectMeta.Annotations["cert-manager.io/common-name"]
		if cn == name {
			log.Infof("syncArgoClusterSecret-> Found!")
			updateRemoteSecret(true, server, argoCreds, secret)
		}
	}

}

func (c *Controller) syncCertSecret(obj interface{}) {
	log.Info("syncCertSecret...")
	secret := obj.(*corev1.Secret)
	if secret == nil {
		log.Warnf("unable to type assert to secret object")
		return
	}

	cn := secret.ObjectMeta.Annotations["cert-manager.io/common-name"]
	log.Infof("onAddCertSecret CN: %s", cn)
	if len(cn) == 0 {
		log.Warnf("No common name found in secret %s/%s", secret.GetNamespace(), secret.GetName())
		return
	}
	log.Info("Now we should get the argo cluster secret from the lister")
	argoSecrets, err := c.argoClusterSecretLister.Secrets(ArgoNs).List(labels.Everything())
	if err != nil {
		log.Errorf("unable to list secret in namesapce %s: %v", ArgoNs, err)
		return
	}
	for _, secret := range argoSecrets {
		name := string(secret.Data["name"])
		if len(name) == 0 {
			log.Warnf("no name definedd in secret. This looks like the 'in-cluster' ArgoCD secret. Skipping it.")
			continue
		}

		if cn == name {
			log.Infof("syncCertSecret -> Found!")
			server := string(secret.Data["server"])
			var argoCreds ArgoClusterCredential
			if err := json.Unmarshal(secret.Data["config"], &argoCreds); err != nil {
				log.Errorf("unable to unmarshal secret to cluster with server %s: %v", server, err)
				return
			}
			updateRemoteSecret(true, server, argoCreds, secret)
		}
	}
}
