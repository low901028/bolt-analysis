package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/boltdb/bolt"
	"io/ioutil"
	"log"
	"os"
)

/* Transaction: 读写事务/只读事务
 * 1. Bolt allows only one read-write transaction at a time
 * 2. Bolt allows as many read-only transactions as you want at a time.
 * Each transaction has a consistent view of the data as it existed when the transaction started.
 * Note: （1）、individual transactions and all objects created from them are not thread safe!!!
 *              To work with data in multiple goroutines you must start a transaction for each one or use locking to ensure onlu one goroute accesses a transaction at a time.
 *              Creating transaction form the DB is thread safe.
 *       （2）、Read-only transaction and read-write transactions should not depend on one another and generally shouldn't be opened simultaneously in the same goroutime. This can cause a deadlock as the read-write transaction needs to periodically re-map the data file
 *              but it cann't do so while a read-only transaction is open.
 */
func main() {
	//read_write_transacton_simple()
	//tx_commit_errTxClosed()
	//tx_rollback_errTxClosed()
	//read_only_transaction()
	//read_only_tx_check()
	//tx_commit_writable()
	tx_cursor()
	//tx_batch_bolt()
}

// 读写事务
func read_write_transacton_simple() {
	// read-write transaction（读写事务）
	db := OpenDB(nil)
	defer db.CloseDB()

	// 开始
	tx, err := db.Begin(true)
	if err != nil {
		log.Fatal(err)
	}

	// 操作
	buc, err := tx.CreateBucketIfNotExists([]byte("bucket_111"))
	if err != nil {
		log.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		hello := fmt.Sprint("hello_", i)
		world := fmt.Sprint("world_", i)
		buc.Put([]byte(hello), []byte(world))
	}

	/*// 查询
	buc = tx.Bucket([]byte("bucket_111"))
	buc.ForEach(func(k, v []byte) error {
		fmt.Println("the bucket <key, value> pair: ",string(k),"=", string(v))
		return nil
	})*/

	// 提交
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}

// 事务commit
func tx_commit_errTxClosed() {
	db := OpenDB(nil)
	defer db.CloseDB()

	tx, err := db.Begin(true)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := tx.CreateBucket([]byte("foo")); err != nil {
		log.Fatal(err)
	}

	tx.Bucket([]byte("foo")).Stats()

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}

	// Note：此处触发异常: tx已关闭 对应的db初始化内容已处理
	if err := tx.Commit(); err != nil {
		log.Fatal("===here===", err)
	}
}

// 事务rollback
func tx_rollback_errTxClosed() {
	db := OpenDB(nil)
	defer db.CloseDB()

	tx, err := db.Begin(true)
	if err != nil {
		log.Fatal("transaction  begin: ", err)
	}

	buc, err := tx.CreateBucket([]byte("test_demo"))

	if buc != nil {
		log.Println("the bucket is created ", buc)
	}

	if err := tx.Rollback(); err != nil {
		log.Println("the tx rollbakc is failure!!!")
	}

	/*if buc := tx.Bucket([]byte("test_demo")); buc !=nil {
		fmt.Println("the tx rollback is failured!")
	}*/
	if err := tx.Rollback(); err == bolt.ErrTxClosed {
		log.Println("the tx rollbakc is failure!!!")
	}
}

// 只读事务
func read_only_transaction() {
	// read-write transaction（读写事务）
	db := OpenDB(&bolt.Options{ReadOnly: true}) // 此处options中read-only设置为true
	//db := OpenDB(nil)
	defer db.CloseDB()

	db.Begin(false) // 事务取消

	if err := db.View(func(tx *bolt.Tx) error {
		buc := tx.Bucket([]byte("bucket_111"))
		buc.ForEach(func(k, v []byte) error {
			fmt.Println("the bucket <key, value> pair: ", string(k), "=", string(v))
			return nil
		})
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

//
func read_only_tx_check() {
	readOnlyDB := OpenDB(&bolt.Options{ReadOnly: true})
	defer readOnlyDB.CloseDB()

	tx, err := readOnlyDB.Begin(false)
	if err != nil {
		log.Println(err)
	}
	numChecks := 2
	errc := make(chan error, numChecks)
	check := func() {
		err, _ := <-tx.Check()
		//fmt.Println( <-ch)
		errc <- err
	}
	for i := 0; i < numChecks; i++ {
		go check()
	}
	for i := 0; i < numChecks; i++ {
		if err := <-errc; err != nil {
			log.Fatal(err)
		}
	}
	// close the view transaction
	tx.Rollback()
}

//
func tx_commit_writable() {
	db := OpenDB(nil)
	defer db.CloseDB()

	tx, err := db.Begin(false) // writable = false
	if err != nil {
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil { // execute commit 出现异常
		log.Fatal(err)
	}
	tx.Rollback()
}

// 游标
func tx_cursor() {
	db := OpenDB(nil)
	defer db.CloseDB()

	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucket([]byte("widgets")); err != nil {
			log.Fatal(err)
		}

		if _, err := tx.CreateBucket([]byte("woojits")); err != nil {
			log.Fatal(err)
		}

		cursor := tx.Cursor()

		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			log.Printf("the kye-value pair <key=%s, value=%s>\n", k, v)
		}

		if k, v := cursor.First(); !bytes.Equal(k, []byte("widgets")) {
			log.Fatalf("unexpected key: %v", string(k))
		} else if v != nil {
			log.Fatalf("unexpected value: %v", string(v))
		}

		if k, v := cursor.Next(); !bytes.Equal(k, []byte("woojits")) {
			log.Fatalf("unexpected key: %v", k)
		} else if v != nil {
			log.Fatalf("unexpected value: %v", v)
		}

		//
		if k, v := cursor.Next(); k != nil {
			log.Fatalf("unexpected key: %v", k)
		} else if v != nil {
			log.Fatalf("unexpected value: %v", k)
		}

		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

// batch
func tx_batch_bolt() {
	db := OpenDB(nil)
	defer db.CloseDB()
	//
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte("hello_world")); err != nil {
			log.Fatal("create bucket is failure ", err)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	// tx, _ := db.Begin(true)
	n := 5000
	ch := make(chan error)
	for i := 0; i < n; i++ {
		go func(i int) {
			ch <- db.Batch(func(tx *bolt.Tx) error {
				return tx.Bucket([]byte("hello_world")).Put(u64tob(uint64(i)), []byte(fmt.Sprint("hello_", i)))
			})
		}(i)
	}

	for i := 0; i < n; i++ {
		if err := <-ch; err != nil {
			log.Fatal(err)
		}
	}

	if err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("hello_world"))
		for i := 0; i < n; i++ {
			v := b.Get(u64tob(uint64(i)))
			fmt.Println("the value is ", string(v))
			if v == nil {
				log.Fatalf("key not found: %d", i)
			}
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	//if err := tx.Commit() ; err != nil{
	//	log.Fatal(err)
	//}
}

//

func u64tob(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// ================================================================ bolt db settings ===================================================================
type DB struct {
	*bolt.DB
}

// open the data file
func OpenDB(options *bolt.Options) *DB {
	db, err := bolt.Open("F:\\bench\\bolt.db", 0666, nil)
	if err != nil {
		panic(err)
	}
	return &DB{db}
}

// bolt data file
func tempfile() string {
	f, err := ioutil.TempFile("", "bolt-")
	if err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
	if err := os.Remove(f.Name()); err != nil {
		panic(err)
	}
	return f.Name()
}

// close the db
func (db *DB) CloseDB() {
	if err := db.Close(); err != nil {
		panic(err)
	}
}
