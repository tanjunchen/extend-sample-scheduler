package controller

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/julienschmidt/httprouter"
	schedulerapi "github.com/kubernetes/k8s.io/kubernetes/pkg/scheduler/api"
)

func Index(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	fmt.Fprint(w, "Welcome to sample-scheduler-extender!\n")
}

func Filter(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	var buf bytes.Buffer
	bofy := io.TeeReader(r.Body,&buf)
	var extenderArgs schedulerapi.ExtenderArgs
	var extenderFilterResult * schedulerapi.ExtenderFilterResult

}
