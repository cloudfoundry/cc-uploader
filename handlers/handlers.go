package handlers

import (
	"net/http"

	"github.com/cloudfoundry-incubator/cc-uploader"
	"github.com/cloudfoundry-incubator/cc-uploader/ccclient"
	"github.com/cloudfoundry-incubator/cc-uploader/handlers/upload_build_artifacts"
	"github.com/cloudfoundry-incubator/cc-uploader/handlers/upload_droplet"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/rata"
)

func New(uploader ccclient.Uploader, poller ccclient.Poller, logger lager.Logger) (http.Handler, error) {
	return rata.NewRouter(ccuploader.Routes, rata.Handlers{
		ccuploader.UploadDropletRoute:        upload_droplet.New(uploader, poller, logger),
		ccuploader.UploadBuildArtifactsRoute: upload_build_artifacts.New(uploader, logger),
	})
}
