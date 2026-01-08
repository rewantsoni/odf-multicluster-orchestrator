package addons

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/red-hat-storage/odf-multicluster-orchestrator/controllers/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type OnboardingSubjectRole string
type OnboardingTicket struct {
	ID                string                `json:"id"`
	ExpirationDate    int64                 `json:"expirationDate,string"`
	SubjectRole       OnboardingSubjectRole `json:"subjectRole"`
	StorageQuotaInGiB *uint                 `json:"storageQuotaInGiB,omitempty"`
	StorageCluster    types.UID             `json:"storageCluster"`
}

func requestStorageClusterPeerToken(ctx context.Context, proxyServiceNamespace string) ([]byte, error) {
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, fmt.Errorf("failed to read token: %w", err)
	}
	url := fmt.Sprintf("https://ux-backend-proxy.%s.svc.cluster.local:8888/onboarding/peer-tokens", proxyServiceNamespace)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", string(token)))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read http response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %s", http.StatusText(resp.StatusCode))
	}

	return body, nil
}

func UnmarshalOnboardingToken(token *corev1.Secret) (*OnboardingTicket, error) {

	ticketArr := strings.Split(string(token.Data[utils.SecretDataKey]), ".")

	message, err := base64.StdEncoding.DecodeString(ticketArr[0])
	if err != nil {
		return nil, err
	}

	var ticketData OnboardingTicket
	err = json.Unmarshal(message, &ticketData)
	if err != nil {
		return nil, err
	}

	return &ticketData, nil
}
