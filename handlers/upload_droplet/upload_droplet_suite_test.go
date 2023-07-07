package upload_droplet_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"testing"
)

func TestUpload_droplet(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Upload Droplet Suite")
}
