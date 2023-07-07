package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"code.cloudfoundry.org/cc-uploader/ccclient"
	"code.cloudfoundry.org/cc-uploader/config"
	"code.cloudfoundry.org/cc-uploader/handlers"
	"code.cloudfoundry.org/cfhttp"
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

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.Parse()

	uploaderConfig, err := config.NewUploaderConfig(*configPath)
	if err != nil {
		panic(err.Error())
	}

	logger, reconfigurableSink := lagerflags.NewFromConfig("cc-uploader", uploaderConfig.LagerConfig)

	initializeDropsonde(logger, uploaderConfig)

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

	err = <-monitor.Wait()
	if err != nil {
		logger.Error("exited-with-failure", err)
		os.Exit(1)
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

	ccUploaderHandler, err := handlers.New(uploader, poller, logger)
	if err != nil {
		logger.Error("router-building-failed", err)
		os.Exit(1)
	}

	if tlsServer {
		tlsConfig, err := cfhttp.NewTLSConfig(
			uploaderConfig.MutualTLS.ServerCert,
			uploaderConfig.MutualTLS.ServerKey,
			uploaderConfig.MutualTLS.CACert)

		if err != nil {
			logger.Error("new-tls-config-failed", err)
			os.Exit(1)
		}

		tlsConfig.MinVersion = tls.VersionTLS12
		tlsConfig.CipherSuites = []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		}

		if err != nil {
			logger.Fatal("failed-loading-tls-config", err)
		}
		return http_server.NewTLSServer(uploaderConfig.MutualTLS.ListenAddress, ccUploaderHandler, tlsConfig)
	}
	return http_server.New(uploaderConfig.ListenAddress, ccUploaderHandler)
}
