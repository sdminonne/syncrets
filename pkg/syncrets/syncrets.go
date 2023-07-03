package syncrets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"time"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"
)

var (
	ArgoNs, CertsNs string
	//cluster2Certs   sync.Map
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
