package fake_cc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"

	"code.cloudfoundry.org/runtimeschema/cc_messages"
	"github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit/http_server"
)

const (
	finishedResponseBody = `
        {
            "metadata":{
                "guid": "inigo-job-guid",
                "url": "/v2/jobs/inigo-job-guid"
            },
            "entity": {
                "status": "finished"
            }
        }
    `
)

type FakeCC struct {
	address string

	UploadedDroplets             map[string][]byte
	UploadedBuildArtifactsCaches map[string][]byte
	stagingGuids                 []string
	stagingResponses             []cc_messages.StagingResponseForCC
	stagingResponseStatusCode    int
	stagingResponseBody          string
	lock                         *sync.RWMutex
	ca_cert                      string
	mtls_cert                    string
	mtls_key                     string
}

func New(address, ca_cert, mtls_cert, mtls_key string) *FakeCC {
	return &FakeCC{
		address: 		      address,
		UploadedDroplets:             map[string][]byte{},
		UploadedBuildArtifactsCaches: map[string][]byte{},
		stagingGuids:                 []string{},
		stagingResponses:             []cc_messages.StagingResponseForCC{},
		stagingResponseStatusCode:    http.StatusOK,
		stagingResponseBody:          "{}",
		lock:                         new(sync.RWMutex),
		ca_cert:                      ca_cert,
		mtls_cert:                    mtls_cert,
		mtls_key:                     mtls_key,
	}
}

func (f *FakeCC) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	cert, err := tls.LoadX509KeyPair(f.mtls_cert, f.mtls_key)
	if err != nil {
		log.Fatalln("Unable to load cert", err)
	}
	caCert, err := ioutil.ReadFile(f.ca_cert)
	if err != nil {
		log.Fatal("Unable to open cert", err)
	}

	clientCertPool := x509.NewCertPool()
	clientCertPool.AppendCertsFromPEM(caCert)
	tlsConfig := &tls.Config{
		InsecureSkipVerify: false,
		Certificates:       []tls.Certificate{cert},
		ClientAuth:         tls.RequireAndVerifyClientCert,
		ClientCAs:          clientCertPool,
		RootCAs:            clientCertPool,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
	}
	err = http_server.NewTLSServer(f.address, f, tlsConfig).Run(signals, ready)

	f.Reset()

	return err
}

func (f *FakeCC) Address() string {
	return "https://" + f.address
}

func (f *FakeCC) Reset() {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.UploadedDroplets = map[string][]byte{}
	f.UploadedBuildArtifactsCaches = map[string][]byte{}
	f.stagingGuids = []string{}
	f.stagingResponses = []cc_messages.StagingResponseForCC{}
	f.stagingResponseStatusCode = http.StatusOK
	f.stagingResponseBody = "{}"
}

func (f *FakeCC) SetStagingResponseStatusCode(statusCode int) {
	f.stagingResponseStatusCode = statusCode
}

func (f *FakeCC) SetStagingResponseBody(body string) {
	f.stagingResponseBody = body
}

func (f *FakeCC) StagingGuids() []string {
	f.lock.RLock()
	defer f.lock.RUnlock()
	return f.stagingGuids
}

func (f *FakeCC) StagingResponses() []cc_messages.StagingResponseForCC {
	f.lock.RLock()
	defer f.lock.RUnlock()
	return f.stagingResponses
}

func (f *FakeCC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(ginkgo.GinkgoWriter, "[FAKE CC] Handling request: %s\n", r.URL.Path)

	endpoints := map[string]func(http.ResponseWriter, *http.Request){
		"/staging/droplets/.*/upload": f.handleDropletUploadRequest,
	}

	for pattern, handler := range endpoints {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(r.URL.Path)
		if matches != nil {
			handler(w, r)
			return
		}
	}

	ginkgo.Fail(fmt.Sprintf("[FAKE CC] No matching endpoint handler for %s", r.URL.Path))
}

func (f *FakeCC) handleDropletUploadRequest(w http.ResponseWriter, r *http.Request) {
	key := getFileUploadKey(r)
	file, _, err := r.FormFile(key)
	Expect(err).NotTo(HaveOccurred())

	uploadedBytes, err := ioutil.ReadAll(file)
	Expect(err).NotTo(HaveOccurred())

	re := regexp.MustCompile("/staging/droplets/(.*)/upload")
	appGuid := re.FindStringSubmatch(r.URL.Path)[1]

	f.UploadedDroplets[appGuid] = uploadedBytes
	fmt.Fprintf(ginkgo.GinkgoWriter, "[FAKE CC] Received %d bytes for droplet for app-guid %s\n", len(uploadedBytes), appGuid)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(finishedResponseBody))
}

func getFileUploadKey(r *http.Request) string {
	err := r.ParseMultipartForm(1024)
	Expect(err).NotTo(HaveOccurred())

	Expect(r.MultipartForm.File).To(HaveLen(1))
	var key string
	for k, _ := range r.MultipartForm.File {
		key = k
	}
	Expect(key).NotTo(BeEmpty())
	return key
}
