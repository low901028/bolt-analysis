package main

import (
	"fmt"
	"github.com/boltdb/bolt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"unsafe"
)

func main() {
	// print the bolt db data file path
	// boltDBInfo()
	// the bolt db exist ?
	// boltDBExist()
	// invalid bolt db
	// boltDBInvalid()
	boltErrVersionMismatch()
}

type meta struct {
	magic    uint32
	version  uint32
	_        uint32
	_        uint32
	_        [16]byte
	_        uint64
	pgid     uint64
	_        uint64
	checksum uint64
}

const pageSize = 4096
const pageHeaderSize = 16

func boltErrVersionMismatch() {
	fmt.Println("the system page size is ", os.Getpagesize())
	if pageSize != os.Getpagesize() {
		log.Print("page size mismatch")
	}

	db := openDB(nil)
	path := db.Path()
	defer db.closeDB()

	if err := db.DB.Close(); err != nil {
		log.Print("close the bolt db is failured!!!")
	}

	buf, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatal("read file is failured! ", err)
	}
	meta0 := (*meta)(unsafe.Pointer(&buf[pageHeaderSize]))
	meta0.version++
	meta1 := (*meta)(unsafe.Pointer(&buf[pageSize+pageHeaderSize]))
	meta1.version++

	//newPath := "F:\\bench\\bolt_1.db"
	if err := ioutil.WriteFile(path, buf, 06666); err != nil {
		log.Fatal("write file is failured!!!", err)
	}

	if _, err := bolt.Open(path, 06666, nil); err != bolt.ErrVersionMismatch {
		log.Fatal("open the bolt database file is wrong!!!", err)
	}
}

func boltDBInvalid() {
	path := "F:\\bench\\bolt_1.db"

	file, err := os.Create(path)
	if err != nil {
		log.Fatal("created the bolt db data file is failured!")
	}

	if _, err := fmt.Fprintln(file, "this is not a bolt database"); err != nil {
		log.Fatal("the file is not a bolt database!!!")
	}

	if err := file.Close(); err != nil {
		log.Fatal("close the database file failured", err)
	}
	//defer file.Close()
	defer os.Remove(path)

	if _, err := bolt.Open(path, 0666, nil); err != nil {
		log.Fatal("open the bolt database is failured ", err)
	}
}

func boltDBExist() {
	db, err := bolt.Open(filepath.Join("F:\\bench", "bolt.db"), 0666, nil)
	defer db.Close()
	if err != nil {
		log.Fatal("the bolt db data file path is wrong!!!, the path = ", filepath.Join("bolt.db", "F:\\bench\\"))
	}
	fmt.Println("the bolt db data file path is ", db.Path())
}

func boltDBInfo() {
	//
	db := openDB(nil)
	defer db.closeDB()

	fmt.Println("the db file path is ", db.Path())
}

// ================================================================ bolt db settings ===================================================================
type BoltDB struct {
	*bolt.DB
}

// open the data file
func openDB(options *bolt.Options) *BoltDB {
	db, err := bolt.Open("F:\\bench\\bolt.db", 0666, nil)
	if err != nil {
		panic(err)
	}
	return &BoltDB{db}
}

// close the db
func (db *BoltDB) closeDB() {
	if err := db.Close(); err != nil {
		panic(err)
	}
}
