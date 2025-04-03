package main

import (
	"code.cloudfoundry.org/tlsconfig"
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/cc-uploader/ccclient"
	"code.cloudfoundry.org/cc-uploader/config"
	"code.cloudfoundry.org/cc-uploader/handlers"
	"code.cloudfoundry.org/debugserver"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerflags"
	"github.com/cloudfoundry/dropsonde"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
)

var configPath = flag.String(
	"configPath",
	"",
	"path to config",
)

const (
	ccUploadDialTimeout         = 10 * time.Second
	ccUploadKeepAlive           = 30 * time.Second
	ccUploadTLSHandshakeTimeout = 10 * time.Second
	dropsondeOrigin             = "cc_uploader"
	communicationTimeout        = 30 * time.Second
)

// Global WaitGroup to track uploads
var uploadWaitGroup sync.WaitGroup

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.Parse()

	uploaderConfig, err := config.NewUploaderConfig(*configPath)
	if err != nil {
		panic(err.Error())
	}

	logger, reconfigurableSink := lagerflags.NewFromConfig("cc-uploader", uploaderConfig.LagerConfig)

	initializeDropsonde(logger, uploaderConfig)

	// Create signal channel to listen for shutdown signals
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Goroutine to log any signal received (without handling non-TERM signals)
	go func() {
		allSignals := make(chan os.Signal, 1)
		signal.Notify(allSignals) // Capture all signals for logging
		for sig := range allSignals {
			logger.Info("received-signal", lager.Data{"signal": sig.String()})
		}
	}()
	var nonTLSServer *http.Server
	tlsServer, tlsRunner := initializeServer(logger, uploaderConfig, true)
	members := grouper.Members{
		{"cc-uploader-tls", tlsRunner},
	}
	if !uploaderConfig.DisableNonTLS {
		var nonTLSRunner ifrit.Runner
		nonTLSServer, nonTLSRunner = initializeServer(logger, uploaderConfig, false)
		members = append(grouper.Members{
			{"cc-uploader", nonTLSRunner},
		}, members...)
	}
	if uploaderConfig.DebugServerConfig.DebugAddress != "" {
		members = append(grouper.Members{
			{"debug-server", debugserver.Runner(uploaderConfig.DebugServerConfig.DebugAddress, reconfigurableSink)},
		}, members...)
	}

	group := grouper.NewOrdered(os.Interrupt, members)

	monitor := ifrit.Invoke(sigmon.New(group))
	logger.Info("ready")

	select {
	case err := <-monitor.Wait(): // Handle process failure
		if err != nil {
			logger.Info("exited-with-failure")
			os.Exit(1)
		}
	case sig := <-signalChan: // Handle shutdown signal
		logger.Info("shutdown-signal-received", lager.Data{"signal": sig})

		// Gracefully signal Ifrit monitor to stop processes
		monitor.Signal(os.Interrupt)
		logger.Info("graceful-shutdown-waiting-for-uploads")
		// Wait for all uploads to finish before shutting down
		uploadWaitGroup.Wait()
		// Add a delay to ensure responses are sent to Diego before shutdown
		extraWait := 30 * time.Second
		logger.Info("waiting-additional-time-before-shutdown", lager.Data{"duration": extraWait})
		time.Sleep(extraWait) // Ensure uploader has time to send responses
		// Gracefully shutdown the HTTP server
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()
		if !uploaderConfig.DisableNonTLS {
			if err := nonTLSServer.Shutdown(ctx); err != nil {
				logger.Error("non-tls-server-shutdown-failed", err)
			}
		}

		if err := tlsServer.Shutdown(ctx); err != nil {
			logger.Error("tls-server-shutdown-failed", err)
		}

	}

	logger.Info("exited")
}

func initializeDropsonde(logger lager.Logger, uploaderConfig config.UploaderConfig) {
	dropsondeDestination := fmt.Sprint("localhost:", uploaderConfig.DropsondePort)
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeTlsTransport(uploaderConfig config.UploaderConfig, skipVerify bool) *http.Transport {
	cert, err := tls.LoadX509KeyPair(uploaderConfig.CCClientCert, uploaderConfig.CCClientKey)
	if err != nil {
		log.Fatalln("Unable to load cert", err)
	}

	clientCACert, err := ioutil.ReadFile(uploaderConfig.CCCACert)
	if err != nil {
		log.Fatal("Unable to open cert", err)
	}

	clientCertPool, err := x509.SystemCertPool()
	if err != nil {
		log.Fatal("Unable to open system certificate pool", err)
	}

	clientCertPool.AppendCertsFromPEM(clientCACert)

	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   ccUploadDialTimeout,
			KeepAlive: ccUploadKeepAlive,
		}).Dial,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: skipVerify,
			Certificates:       []tls.Certificate{cert},
			RootCAs:            clientCertPool,
		},
		TLSHandshakeTimeout: ccUploadTLSHandshakeTimeout,
	}
}

func initializeServer(logger lager.Logger, uploaderConfig config.UploaderConfig, tlsServer bool) (*http.Server, ifrit.Runner) {
	uploader := ccclient.NewUploader(logger, &http.Client{Transport: initializeTlsTransport(uploaderConfig, false)})

	// To maintain backwards compatibility with hairpin polling URLs, skip SSL verification for now
	poller := ccclient.NewPoller(logger, &http.Client{Transport: initializeTlsTransport(uploaderConfig, true)}, time.Duration(uploaderConfig.CCJobPollingInterval))

	ccUploaderHandler, err := handlers.New(uploader, poller, logger, &uploadWaitGroup)
	if err != nil {
		logger.Error("router-building-failed", err)
		os.Exit(1)
	}

	var server *http.Server
	if tlsServer {
		clientTLSConfig, err := tlsconfig.Build(
			tlsconfig.WithIdentityFromFile(uploaderConfig.MutualTLS.ServerCert, uploaderConfig.MutualTLS.ServerKey),
		).Client(tlsconfig.WithAuthorityFromFile(uploaderConfig.MutualTLS.CACert))

		if err != nil {
			logger.Error("new-tls-config-failed", err)
			os.Exit(1)
		}

		clientTLSConfig.MinVersion = tls.VersionTLS12
		clientTLSConfig.CipherSuites = []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		}

		if err != nil {
			logger.Fatal("failed-loading-tls-config", err)
		}
		server = &http.Server{
			Addr:      uploaderConfig.MutualTLS.ListenAddress,
			Handler:   ccUploaderHandler,
			TLSConfig: clientTLSConfig,
		}
		return server, http_server.NewTLSServer(uploaderConfig.MutualTLS.ListenAddress, ccUploaderHandler, clientTLSConfig)
	}
	server = &http.Server{
		Addr:    uploaderConfig.ListenAddress,
		Handler: ccUploaderHandler,
	}
	return server, http_server.New(uploaderConfig.ListenAddress, ccUploaderHandler)
}
