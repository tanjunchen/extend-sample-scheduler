package main

import (
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/julienschmidt/httprouter"
)

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

func main() {
	router := httprouter.New()
	router.GET("/", controller.Index)
	router.GET("/filter", controller.Filter)
	router.GET("/prioritize", controller.Prioritize)

	log.Printf("start up sample-scheduler-extender!\n")
	log.Fatal(http.ListenAndServe(":8888", router))
}
