package main_test

import (
	"code.cloudfoundry.org/cc-uploader/ccclient/fake_cc"

	. "github.com/onsi/ginkgo"
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

func TestCCUploader(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CC Uploader Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {

	ccUploaderPath, err := gexec.Build("code.cloudfoundry.org/cc-uploader/cmd/cc-uploader")
	Expect(err).NotTo(HaveOccurred())
	return []byte(ccUploaderPath)
}, func(ccUploaderPath []byte) {

	ccUploaderBinary = string(ccUploaderPath)
})

var _ = SynchronizedAfterSuite(func() {
}, func() {
	gexec.CleanupBuildArtifacts()
})
