package main

import (
	"errors"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/castai/azure-spot-handler/castai"
	"github.com/castai/azure-spot-handler/config"
	"github.com/castai/azure-spot-handler/handler"
	"github.com/castai/azure-spot-handler/version"
)

var (
	GitCommit = "undefined"
	GitRef    = "no-ref"
	Version   = "local"
)

func main() {
	cfg := config.Get()

	logger := logrus.New()
	log := logrus.WithFields(logrus.Fields{})

	kubeconfig, err := retrieveKubeConfig(log)
	if err != nil {
		log.Fatalf("err retrieving kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		log.Fatalf("err creating clientset: %v", err)
	}

	k8sVersion, err := version.Get(clientset)
	if err != nil {
		log.Fatalf("failed getting kubernetes version: %v", err)
	}

	handlerVersion := &version.HandlerVersion{
		GitCommit: GitCommit,
		GitRef:    GitRef,
		Version:   Version,
	}

	log = logger.WithFields(logrus.Fields{
		"version":     handlerVersion,
		"k8s_version": k8sVersion.Full(),
	})

	interruptChecker, err := buildInterruptChecker(cfg.Provider)
	if err != nil {
		log.Fatalf("interrupt checker: %v", err)
	}

	// Set 5 seconds until we timeout calling mothership and retry.
	castHttpClient := castai.NewDefaultClient(
		cfg.APIUrl,
		cfg.APIKey,
		logrus.Level(cfg.LogLevel),
		5*time.Second,
		Version,
	)
	castClient := castai.NewClient(logger, castHttpClient, cfg.ClusterID)

	spotHandler := handler.NewSpotHandler(
		log,
		castClient,
		clientset,
		interruptChecker,
		time.Duration(cfg.PollIntervalSeconds)*time.Second,
		cfg.NodeName,
	)

	if cfg.PprofPort != 0 {
		go func() {
			addr := fmt.Sprintf(":%d", cfg.PprofPort)
			log.Infof("starting pprof server on %s", addr)
			if err := http.ListenAndServe(addr, http.DefaultServeMux); err != nil {
				log.Errorf("failed to start pprof http server: %v", err)
			}
		}()
	}

	log.Infof("running spot handler, provider=%s", cfg.Provider)
	if err := spotHandler.Run(signals.SetupSignalHandler()); err != nil {
		logErr := &logContextErr{}
		if errors.As(err, &logErr) {
			log = logger.WithFields(logErr.fields)
		}
		log.Fatalf("spot handler failed: %v", err)
	}
}

func buildInterruptChecker(provider string) (handler.InterruptChecker, error) {
	switch provider {
	case "azure":
		return handler.NewAzureInterruptChecker(), nil
	case "gcp":
		return handler.NewGCPChecker(), nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

func retrieveKubeConfig(log logrus.FieldLogger) (*rest.Config, error) {
	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	log.Debug("using in cluster kubeconfig")
	return inClusterConfig, nil
}

type logContextErr struct {
	err    error
	fields logrus.Fields
}

func (e *logContextErr) Error() string {
	return e.err.Error()
}

func (e *logContextErr) Unwrap() error {
	return e.err
}
