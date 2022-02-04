package castai

import (
	"context"
	"fmt"
	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
	"net/http"
	"time"
)

const headerAPIKey = "X-API-Key"

type Client interface {
	SendCloudEvent(ctx context.Context, req *CloudEventRequest) error
}

func NewClient(log *logrus.Logger, rest *resty.Client, clusterID string) Client {
	return &client{
		log:       log,
		rest:      rest,
		clusterID: clusterID,
	}
}

// NewDefaultClient configures a default instance of the resty.Client used to do HTTP requests.
func NewDefaultClient(url, key string, level logrus.Level, timeout time.Duration) *resty.Client {
	client := resty.New()
	client.SetHostURL(url)
	client.SetTimeout(timeout)
	client.Header.Set(http.CanonicalHeaderKey(headerAPIKey), key)
	if level == logrus.TraceLevel {
		client.SetDebug(true)
	}

	return client
}

type client struct {
	log       *logrus.Logger
	rest      *resty.Client
	clusterID string
	nodeID    string
}

type CloudEventRequest struct {
	EventType string `json:"event_type"`
	NodeID    string `json:"node_id"`
}

func (c *client) SendCloudEvent(ctx context.Context, req *CloudEventRequest) error {
	resp, err := c.rest.R().
		SetBody(req).
		SetContext(ctx).
		Post(fmt.Sprintf("/v1/kubernetes/external-clusters/%s/events", c.clusterID))

	if err != nil {
		return fmt.Errorf("sending aks spot interrupt: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("sending aks spot interrupt: request error status_code=%d body=%s", resp.StatusCode(), resp.Body())
	}

	return nil
}