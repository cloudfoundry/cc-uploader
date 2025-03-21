package main

import (
	"code.cloudfoundry.org/tlsconfig"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
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

	members := grouper.Members{
		{"cc-uploader-tls", initializeServer(logger, uploaderConfig, true)},
	}
	if !uploaderConfig.DisableNonTLS {
		members = append(grouper.Members{
			{"cc-uploader", initializeServer(logger, uploaderConfig, false)},
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
		logger.Info("all-uploads-completed, waiting before shutdown")
		time.Sleep(20 * time.Second)
		logger.Info("proceeding with shutdown")
		logger.Info("all-uploads-completed, shutting down")

		logger.Info("graceful-shutdown-completed")
		pid := os.Getpid()

		logger.Info("Forcefully terminate if necessary")
		forceShutdown(pid, "cc-uploader", logger)
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

func initializeServer(logger lager.Logger, uploaderConfig config.UploaderConfig, tlsServer bool) ifrit.Runner {
	uploader := ccclient.NewUploader(logger, &http.Client{Transport: initializeTlsTransport(uploaderConfig, false)})

	// To maintain backwards compatibility with hairpin polling URLs, skip SSL verification for now
	poller := ccclient.NewPoller(logger, &http.Client{Transport: initializeTlsTransport(uploaderConfig, true)}, time.Duration(uploaderConfig.CCJobPollingInterval))
	// Wrap the poller to track job completion
	trackedPoller := trackPollingCompletion(poller, logger)

	ccUploaderHandler, err := handlers.New(uploader, trackedPoller, logger)
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

		if err != nil {
			logger.Fatal("failed-loading-tls-config", err)
		}
		return http_server.NewTLSServer(uploaderConfig.MutualTLS.ListenAddress, ccUploaderHandler, clientTLSConfig)
	}
	return http_server.New(uploaderConfig.ListenAddress, ccUploaderHandler)
}

// Wrap poller to track when jobs start and finish
func trackPollingCompletion(poller ccclient.Poller, logger lager.Logger) ccclient.Poller {
	return &trackedPoller{
		Poller: poller,
		logger: logger.Session("tracked-poller"),
	}
}

type trackedPoller struct {
	ccclient.Poller
	logger lager.Logger
}

func (tp *trackedPoller) Poll(uploadUrl *url.URL, uploadResponse *http.Response, cancelChan <-chan struct{}) error {
	uploadWaitGroup.Add(1)       // Increase count when polling starts
	defer uploadWaitGroup.Done() // Ensures it always decrements, even on failure

	err := tp.Poller.Poll(uploadUrl, uploadResponse, cancelChan)
	if err != nil {
		tp.logger.Error("polling-failed", err)
		return err
	}

	tp.logger.Info("polling-succeeded")
	return nil
}

func forceShutdown(pid int, processName string, logger lager.Logger) {
	// Check if process is already terminated
	process, err := os.FindProcess(pid)
	if err != nil {
		logger.Info(fmt.Sprintf("Process '%s' with pid '%d' already terminated.", processName, pid))
		return
	}

	logger.Info(fmt.Sprintf("Forcefully shutting down process '%s' with pid '%d'", processName, pid))

	// Send SIGKILL to forcefully terminate the process
	time.Sleep(5 * time.Second)
	err = process.Kill()
	if err != nil {
		logger.Error("failed-to-forcefully-shutdown-process", err, lager.Data{
			"process": processName,
			"pid":     pid,
		})
	}
}
