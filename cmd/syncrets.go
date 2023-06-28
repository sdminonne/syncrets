package cmd

import (
	// Guess we need to do this...
	//routev1 "github.com/openshift/api/route/v1"
	//routev1client "github.com/openshift/client-go/route/clientset/versioned"

	"syncrets/pkg/syncrets"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "syncrets",
	Short: "syncrets syncronize secrets across clusters",
	Long:  `Really you need more?`,
	Run: func(cmd *cobra.Command, args []string) {
		syncrets.DoTheJob2()
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&syncrets.ArgoNs, "argonamespace", "a", "argocd",
		"Namespace where argo cluster secrets wil be watched")
	rootCmd.PersistentFlags().StringVarP(&syncrets.CertsNs, "certnamespace", "c", "cert-manager",
		"Namespace where cert-manager secrets wil be watched")
}
