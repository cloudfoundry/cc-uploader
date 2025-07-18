package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/debugserver"
	"code.cloudfoundry.org/tlsconfig"

	"code.cloudfoundry.org/cc-uploader/ccclient"
	"code.cloudfoundry.org/cc-uploader/config"
	"code.cloudfoundry.org/cc-uploader/handlers"
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

var shutdownTimeoutInMinutes = flag.Int(
	"shutdownTimeoutInMinutes",
	15,
	"max time (minutes) to wait for graceful shutdown",
)

const (
	ccUploadDialTimeout         = 10 * time.Second
	ccUploadKeepAlive           = 30 * time.Second
	ccUploadTLSHandshakeTimeout = 10 * time.Second
	dropsondeOrigin             = "cc_uploader"
	communicationTimeout        = 30 * time.Second
)

var (
	// Global WaitGroup to track uploads
	uploadWaitGroup sync.WaitGroup
)

func newShutdownSignalChannel() <-chan os.Signal {
	// Create signal channel to listen for shutdown signals
	s := make(chan os.Signal, 1)
	signal.Notify(s, os.Interrupt, syscall.SIGTERM)
	return s
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

	clientCACert, err := os.ReadFile(uploaderConfig.CCCACert)
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

func initializeServer(logger lager.Logger, uploaderConfig config.UploaderConfig, tlsServer bool) ifrit.Runner {
	uploader := ccclient.NewUploader(logger, &http.Client{Transport: initializeTlsTransport(uploaderConfig, false)})

	// To maintain backwards compatibility with hairpin polling URLs, skip SSL verification for now
	poller := ccclient.NewPoller(logger, &http.Client{Transport: initializeTlsTransport(uploaderConfig, true)}, time.Duration(uploaderConfig.CCJobPollingInterval))

	ccUploaderHandler, err := handlers.New(uploader, poller, logger, &uploadWaitGroup)
	if err != nil {
		logger.Error("router-building-failed", err)
		os.Exit(1)
	}

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

		return http_server.NewTLSServer(uploaderConfig.MutualTLS.ListenAddress, ccUploaderHandler, clientTLSConfig)
	}

	return http_server.New(uploaderConfig.ListenAddress, ccUploaderHandler)
}

func waitForDrainingToFinish() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		uploadWaitGroup.Wait() // wait for all in-flight uploads to call Done()
		close(done)            // signal completion
	}()
	return done
}

func configureServers(logger lager.Logger, uploaderConfig config.UploaderConfig, reconfigurableSink *lager.ReconfigurableSink) ifrit.Process {

	var nonTLSRunner ifrit.Runner
	tlsRunner := initializeServer(logger, uploaderConfig, true)
	members := grouper.Members{
		{Name: "cc-uploader-tls", Runner: tlsRunner},
	}
	if !uploaderConfig.DisableNonTLS {
		nonTLSRunner = initializeServer(logger, uploaderConfig, false)
		members = append(grouper.Members{
			{Name: "cc-uploader", Runner: nonTLSRunner},
		}, members...)
	}
	if uploaderConfig.DebugServerConfig.DebugAddress != "" {
		members = append(grouper.Members{
			{Name: "debug-server", Runner: debugserver.Runner(uploaderConfig.DebugServerConfig.DebugAddress, reconfigurableSink)},
		}, members...)
	}

	group := grouper.NewOrdered(os.Interrupt, members)
	monitor := ifrit.Invoke(sigmon.New(group))
	logger.Info("ready")

	return monitor
}

func main() {
	flag.Parse()

	uploaderConfig, err := config.NewUploaderConfig(*configPath)
	if err != nil {
		panic(err.Error())
	}

	logger, reconfigurableSink := lagerflags.NewFromConfig("cc-uploader", uploaderConfig.LagerConfig)

	initializeDropsonde(logger, uploaderConfig)

	shutdownSignal := newShutdownSignalChannel()

	monitor := configureServers(logger, uploaderConfig, reconfigurableSink)

	select {
	case err := <-monitor.Wait():
		if err != nil {
			logger.Info("server-exited-with-failure")
			os.Exit(1)
		}
	case s := <-shutdownSignal:
		logger.Info("shutdown-signal-received", lager.Data{"signal": s})

		// Stop accepting new connections on both runners (TLS & non-TLS), Ifrit will close the listeners
		monitor.Signal(os.Interrupt)

		// Create channel to signal when uploads (including polling) are done
		done := waitForDrainingToFinish()

		select {
		case <-done:
			logger.Info("all-uploads-finished")
		case <-time.After(time.Duration(*shutdownTimeoutInMinutes) * time.Minute):
			logger.Info("graceful-shutdown-timed-out",
				lager.Data{"timeout": fmt.Sprintf("%d minutes", *shutdownTimeoutInMinutes)})
		}
	}

	logger.Info("exited")
}
