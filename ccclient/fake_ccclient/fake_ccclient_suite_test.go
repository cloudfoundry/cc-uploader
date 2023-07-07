package fake_ccclient_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"testing"
)

func TestFakeCcclient(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "FakeCcclient Suite")
}
