package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"

	"github.com/castai/azure-spot-handler/castai"
)

const CastNodeIDLabel = "provisioner.cast.ai/node-id"

type InterruptChecker interface {
	Check(ctx context.Context) (bool, error)
}

type SpotHandler struct {
	castClient       castai.Client
	clientset        kubernetes.Interface
	interruptChecker InterruptChecker
	nodeName         string
	pollWaitInterval time.Duration
	log              logrus.FieldLogger
}

func NewSpotHandler(
	log logrus.FieldLogger,
	castClient castai.Client,
	clientset kubernetes.Interface,
	interruptChecker InterruptChecker,
	pollWaitInterval time.Duration,
	nodeName string,
) *SpotHandler {
	return &SpotHandler{
		castClient:       castClient,
		clientset:        clientset,
		interruptChecker: interruptChecker,
		log:              log,
		nodeName:         nodeName,
		pollWaitInterval: pollWaitInterval,
	}
}

func (g *SpotHandler) Run(ctx context.Context) error {
	t := time.NewTicker(g.pollWaitInterval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			// check interruption
			err := func() error {
				interrupted, err := g.interruptChecker.Check(ctx)
				if err != nil {
					return err
				}
				if interrupted {
					g.log.Infof("preemption notice received")
					err := g.handleInterruption(ctx)
					if err != nil {
						return err
					}
					// stop after ACK
					t.Stop()
				}
				return nil
			}()

			if err != nil {
				g.log.Errorf("checking for interruption: %v", err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (g *SpotHandler) handleInterruption(ctx context.Context) error {
	node, err := g.clientset.CoreV1().Nodes().Get(ctx, g.nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	req := &castai.CloudEventRequest{
		EventType: "interrupted",
		NodeID:    node.Labels[CastNodeIDLabel],
	}

	err = g.castClient.SendCloudEvent(ctx, req)
	if err != nil {
		return err
	}

	return g.taintNode(ctx, node)
}

func (g *SpotHandler) taintNode(ctx context.Context, node *v1.Node) error {
	if node.Spec.Unschedulable {
		return nil
	}

	err := g.patchNode(ctx, node, func(n *v1.Node) error {
		n.Spec.Unschedulable = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("patching node unschedulable: %w", err)
	}
	return nil
}

func (g *SpotHandler) patchNode(ctx context.Context, node *v1.Node, changeFn func(*v1.Node) error) error {
	oldData, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("marshaling old data: %w", err)
	}

	if err := changeFn(node); err != nil {
		return err
	}

	newData, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("marshaling new data: %w", err)
	}

	patch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, node)
	if err != nil {
		return fmt.Errorf("creating patch for node: %w", err)
	}

	err = backoff.Retry(func() error {
		_, err = g.clientset.CoreV1().Nodes().Patch(ctx, node.Name, apitypes.StrategicMergePatchType, patch, metav1.PatchOptions{})
		return err
	}, defaultBackoff(ctx))
	if err != nil {
		return fmt.Errorf("patching node: %w", err)
	}

	return nil
}

func defaultBackoff(ctx context.Context) backoff.BackOffContext {
	return backoff.WithContext(backoff.WithMaxRetries(backoff.NewConstantBackOff(1*time.Second), 5), ctx)
}
