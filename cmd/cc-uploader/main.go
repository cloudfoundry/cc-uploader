package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/tlsconfig"

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

var shutdownTimeoutInMinutes = flag.Duration(
	"shutdownTimeoutInMinutes",
	15*time.Minute,
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

func createShutdownSignal() <-chan os.Signal {
	// Create signal channel to listen for shutdown signals
	s := make(chan os.Signal, 1)
	signal.Notify(s, os.Interrupt, syscall.SIGTERM)
	return s
}

func startServersShutdownProcess(ctx context.Context, logger lager.Logger, tlsServer, nonTLSServer *http.Server) {

	wg := &sync.WaitGroup{}

	if nonTLSServer != nil {
		logger.Info("shutting-down-nonTLS-server")
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := nonTLSServer.Shutdown(ctx); err != nil {
				logger.Error("non-tls-server-shutdown-failed", err)
			}
		}()
	}
	if tlsServer != nil {
		logger.Info("shutting-down-tls-server")
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tlsServer.Shutdown(ctx); err != nil {
				logger.Error("tls-server-shutdown-failed", err)
			}
		}()
	}

	wg.Wait()

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

func waitForDrainingToFinish() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		uploadWaitGroup.Wait() // wait for all in-flight uploads to call Done()
		close(done)            // signal completion
	}()
	return done
}

func configureServers(logger lager.Logger, uploaderConfig config.UploaderConfig, reconfigurableSink *lager.ReconfigurableSink) (ifrit.Process, *http.Server, *http.Server) {

	var nonTLSServer *http.Server
	tlsServer, tlsRunner := initializeServer(logger, uploaderConfig, true)
	members := grouper.Members{
		{Name: "cc-uploader-tls", Runner: tlsRunner},
	}
	if !uploaderConfig.DisableNonTLS {
		var nonTLSRunner ifrit.Runner
		nonTLSServer, nonTLSRunner = initializeServer(logger, uploaderConfig, false)
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

	return monitor, tlsServer, nonTLSServer
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.Parse()

	uploaderConfig, err := config.NewUploaderConfig(*configPath)
	if err != nil {
		panic(err.Error())
	}

	logger, reconfigurableSink := lagerflags.NewFromConfig("cc-uploader", uploaderConfig.LagerConfig)

	initializeDropsonde(logger, uploaderConfig)

	shutdownSignal := createShutdownSignal()

	monitor, tlsServer, nonTLSServer := configureServers(logger, uploaderConfig, reconfigurableSink)

	select {
	case err := <-monitor.Wait():
		if err != nil {
			logger.Info("server-exited-with-failure")
			os.Exit(1)
		}
	case s := <-shutdownSignal:
		logger.Info("shutdown-signal-received", lager.Data{"signal": s})

		shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeoutInMinutes)
		defer cancel()

		startServersShutdownProcess(shutdownCtx, logger, tlsServer, nonTLSServer)

		// Create channel to signal when uploads (including polling) are done
		done := waitForDrainingToFinish()

		select {
		case <-done:
			logger.Info("all-uploads-finished")
		case <-shutdownCtx.Done():
			logger.Info("graceful-shutdown-timed-out",
				lager.Data{"timeout": shutdownTimeoutInMinutes.String(), "error": shutdownCtx.Err()})
		}

		monitor.Signal(os.Interrupt)
	}

	logger.Info("exited")
}
