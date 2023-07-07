package upload_build_artifacts_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"testing"
)

func TestUpload_build_artifacts(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Upload Build Artifacts Suite")
}
