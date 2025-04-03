package handlers

import (
	"net/http"
	"sync"

	"code.cloudfoundry.org/cc-uploader"
	"code.cloudfoundry.org/cc-uploader/ccclient"
	"code.cloudfoundry.org/cc-uploader/handlers/upload_build_artifacts"
	"code.cloudfoundry.org/cc-uploader/handlers/upload_droplet"
	"code.cloudfoundry.org/lager/v3"
	"github.com/tedsuo/rata"
)

func New(uploader ccclient.Uploader, poller ccclient.Poller, logger lager.Logger, uploadWaitGroup *sync.WaitGroup) (http.Handler, error) {
	return rata.NewRouter(ccuploader.Routes, rata.Handlers{
		ccuploader.UploadDropletRoute:        upload_droplet.New(uploader, poller, logger, uploadWaitGroup),
		ccuploader.UploadBuildArtifactsRoute: upload_build_artifacts.New(uploader, logger),
	})
}
