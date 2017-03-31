package config_test

import (
	"time"

	. "code.cloudfoundry.org/cc-uploader/config"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Config", func() {
	Context("Uploader config", func() {
		It("generates a config with the default values", func() {
			uploaderConfig, err := NewUploaderConfig("../fixtures/empty_config.json")
			Expect(err).ToNot(HaveOccurred())

			Expect(uploaderConfig.DropsondePort).To(Equal(3457))
			Expect(uploaderConfig.LagerConfig.LogLevel).To(Equal("info"))
			Expect(uploaderConfig.ListenAddress).To(Equal("0.0.0.0:9090"))
			Expect(uploaderConfig.CCJobPollingInterval).To(Equal(Duration(1 * time.Second)))
		})

		It("reads from the config file and populates the config", func() {
			uploaderConfig, err := NewUploaderConfig("../fixtures/cc_uploader_config.json")
			Expect(err).ToNot(HaveOccurred())

			Expect(uploaderConfig.DropsondePort).To(Equal(12))
			Expect(uploaderConfig.LagerConfig.LogLevel).To(Equal("fatal"))
			Expect(uploaderConfig.ListenAddress).To(Equal("listen_addr"))
			Expect(uploaderConfig.CCJobPollingInterval).To(Equal(Duration(5 * time.Second)))
			Expect(uploaderConfig.ConsulCluster).To(Equal("consul_cluster"))
			Expect(uploaderConfig.DebugServerConfig.DebugAddress).To(Equal("debug_address"))
			Expect(uploaderConfig.CCClientCert).To(Equal("/path/to/server.cert"))
			Expect(uploaderConfig.CCClientKey).To(Equal("/path/to/server.key"))
			Expect(uploaderConfig.CCCACert).To(Equal("/path/to/server-ca.cert"))
			Expect(uploaderConfig.MutualTLS.ListenAddress).To(Equal("mtls_listen_addr"))
			Expect(uploaderConfig.MutualTLS.CACert).To(Equal("ca-cert"))
			Expect(uploaderConfig.MutualTLS.ServerCert).To(Equal("server-cert"))
			Expect(uploaderConfig.MutualTLS.ServerKey).To(Equal("server-key"))
		})
	})
})
