package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	"code.cloudfoundry.org/debugserver"
	"code.cloudfoundry.org/lager/lagerflags"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}

	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}

	*d = Duration(dur)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	t := time.Duration(d)
	return []byte(fmt.Sprintf(`"%s"`, t.String())), nil
}

type UploaderConfig struct {
	ConsulCluster        string                        `json:"consul_cluster"`
	DebugServerConfig    debugserver.DebugServerConfig `json:"debug_server_config"`
	DropsondePort        int                           `json:"dropsonde_port"`
	LagerConfig          lagerflags.LagerConfig        `json:"lager_config"`
	ListenAddress        string                        `json:"listen_addr"`
	SkipCertVerify       bool                          `json:"skip_cert_verify"`
	CCJobPollingInterval Duration                      `json:"job_polling_interval"`
}

func DefaultUploaderConfig() UploaderConfig {
	return UploaderConfig{
		DropsondePort:        3457,
		LagerConfig:          lagerflags.DefaultLagerConfig(),
		SkipCertVerify:       false,
		ListenAddress:        "0.0.0.0:9090",
		CCJobPollingInterval: Duration(1 * time.Second),
	}
}

func NewUploaderConfig(configPath string) (UploaderConfig, error) {
	configFile, err := ioutil.ReadFile(configPath)
	if err != nil {
		return UploaderConfig{}, err
	}

	uploaderConfig := DefaultUploaderConfig()

	err = json.Unmarshal(configFile, &uploaderConfig)
	if err != nil {
		return UploaderConfig{}, err
	}

	return uploaderConfig, nil
}
