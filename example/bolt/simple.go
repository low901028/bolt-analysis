package main

import (
	"github.com/boltdb/bolt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"time"
)

/*
*  open the db data file
*   note: it will be created if it doesn't exist.
*         bolt obtains a file lock on the data file so multiple process cann't open the same database at the same time.Opening an already open Blot database will cause it to hang until the other process closes it.
*   options: timeout to prevent an indefinite wait you can pass a timeout option to the Open() function:
 */
func main() {
	ch := make(chan int)

	go func() {
		log.Println(http.ListenAndServe(":10000", nil))
	}()

	db, err := bolt.Open("F:\\bench\\bolt_1.db", // db file
		0666, // file model
		&bolt.Options{Timeout: 1 * time.Second}) // options parameter：timeout
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// operation
	// ...
	ch <- 1
}
