package main_test

import (
	"fmt"
	"os"

	"code.cloudfoundry.org/cc-uploader/ccclient/fake_cc"
	"code.cloudfoundry.org/consuladapter/consulrunner"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/tedsuo/ifrit"

	"testing"
)

var ccUploaderBinary string
var fakeCCTLS *fake_cc.FakeCC
var fakeCC *fake_cc.FakeCC
var fakeCCProcess ifrit.Process
var fakeCCTLSProcess ifrit.Process
var consulRunner *consulrunner.ClusterRunner

func TestCCUploader(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CC Uploader Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	ccUploaderPath, err := gexec.Build("code.cloudfoundry.org/cc-uploader/cmd/cc-uploader")
	Expect(err).NotTo(HaveOccurred())
	return []byte(ccUploaderPath)
}, func(ccUploaderPath []byte) {
	fakeCCTLSAddress := fmt.Sprintf("127.0.0.1:%d", 6767+GinkgoParallelNode())
	fakeCCTLS = fake_cc.NewTLS(
		fakeCCTLSAddress,
		"../../fixtures/cc_uploader_ca_cn.crt",
		"../../fixtures/cc_cn.crt",
		"../../fixtures/cc_cn.key",
	)

	fakeCCAddress := fmt.Sprintf("127.0.0.1:%d", 6767+GinkgoParallelNode())
	fakeCC = fake_cc.New(fakeCCAddress)

	ccUploaderBinary = string(ccUploaderPath)

	consulRunner = consulrunner.NewClusterRunner(
		9001+config.GinkgoConfig.ParallelNode*consulrunner.PortOffsetLength,
		1,
		"http",
	)

	consulRunner.Start()
	consulRunner.WaitUntilReady()
})

var _ = SynchronizedAfterSuite(func() {
	consulRunner.Stop()
}, func() {
	gexec.CleanupBuildArtifacts()
})

var _ = BeforeEach(func() {
	consulRunner.Reset()
	fakeCCProcess = ifrit.Envoke(fakeCC)
	fakeCCTLSProcess = ifrit.Envoke(fakeCCTLS)
})

var _ = AfterEach(func() {
	fakeCCProcess.Signal(os.Kill)
	fakeCCTLSProcess.Signal(os.Kill)
	Eventually(fakeCCProcess.Wait()).Should(Receive(BeNil()))
	Eventually(fakeCCTLSProcess.Wait()).Should(Receive(BeNil()))
})
