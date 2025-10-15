package utils

import (
	"errors"
	"fmt"
)

const (
	MCVNameTemplate         = "odf-multicluster-mcv-%s"
	TokenExchangeDeployment = "token-exchange-agent"
)

var ErrRequeueReconcile = errors.New("requeue reconcile request")

func GetManagedClusterViewName(clusterName string) string {
	return fmt.Sprintf(MCVNameTemplate, clusterName)
}

func GetTokenExchangeManagedClusterViewName(clusterName string) string {
	return fmt.Sprintf("%s-%s", GetManagedClusterViewName(clusterName), TokenExchangeDeployment)
}
