### 简介
Bolt 是由 Howard Chu 的 LMDB 项目启发的一个纯粹的 Go key/value数据库。 该项目的目标是为不需要完整数据库服务器（如Postgres或MySQL）的项目提供一个简单，快速和可靠的数据库。

由于 Bolt 是用来作为这样一个低层次的功能，简单是关键。 该API将是小的，只专注于获取值和设置值而已。

### 项目状态
Blot 稳定，API固定，文件格式固定。 使用完整的单元测试覆盖率和随机黑箱测试来确保数据库一致性和线程安全性。 Blot 目前用于高达1TB的高负载生产环境。 Shopify 和 Heroku等许多公司每天都使用 Bolt 来支持服务。

### A message from the author
Bolt 最初的目标是提供一个简单的纯 Go key/value 存储，并且不会使代码具有多余的特性。为此，该项目取得了成功。但是，这个范围有限也意味着该项目已经完成。

维护一个开源数据库需要大量的时间和精力。 对代码的更改可能会产生意想不到的甚至是灾难性的影响，因此即使是简单的更改也需要经过数小时的仔细测试和验证。

不幸的是，我不再有时间或精力来继续这项工作。 Blot 处于稳定状态，并有多年的成功生产使用。因此，我觉得把它放在现在的状态是最谨慎的做法。

如果你有兴趣使用一个更有特色的Bolt版本，我建议你看一下名为 bbolt的 CoreOS fork。

### Getting Started
#### 安装
为了使用 Blot，首先安装 go 环境，然后执行如下命令：

$ go get github.com/boltdb/bolt/...
该命令将检索库并将Blot可执行文件安装到 $GOBIN 路径中。

#### 打开BlotDB
Bolt中的顶​​级对象是一个DB。它被表示为磁盘上的单个文件，表示数据的一致快照。

要打开数据库，只需使用 bolt.Open() 函数：
package main

import (
    "log"

    "github.com/boltdb/bolt"
)

func main() {
    // Open the my.db data file in your current directory.
    // It will be created if it doesn't exist.
    
    db, err := bolt.Open("my.db", 0600, nil)
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    ...
}
##### 请注意: 
Bolt 会在数据文件上获得一个文件锁，所以多个进程不能同时打开同一个数据库。 打开一个已经打开的 Bolt 数据库将导致它挂起，直到另一个进程关闭它。为防止无限期等待，您可以将超时选项传递给Open()函数：

db, err := bolt.Open("my.db", 0600, &bolt.Options{Timeout: 1 * time.Second})
#### 事务
Bolt 一次只允许一个读写事务，但是一次允许多个只读事务。 每个事务处理都有一个始终如一的数据视图。

单个事务以及从它们创建的所有对象（例如bucket，key）不是线程安全的。 要处理多个goroutine 中的数据，您必须为每个 goroutine 启动一个事务，或使用锁来确保一次只有一个 goroutine 访问事务。 从 DB 创建事务是线程安全的。
 
只读事务和读写事务不应该相互依赖，一般不应该在同一个例程中同时打开。 这可能会导致死锁，因为读写事务需要定期重新映射数据文件，但只有在只读事务处于打开状态时才能这样做。

#### 读写事务
为了启动一个读写事物，你可以使用DB.Update()函数：

err := db.Update(func(tx *bolt.Tx) error {
    ...
    return nil
})
在闭包内部，您有一个一致的数据库视图。 您通过返回零来完成交易。 您也可以通过返回错误来随时回滚事务。 所有的数据库操作都允许在一个读写事务中。

总是检查返回错误，因为它会报告任何可能导致您的交易不完成的磁盘故障。如果你在闭包中返回一个错误，它将被传递。

#### 只读事务
为了启动一个只读事务，你可以使用DB.View()函数：

err := db.View(func(tx *bolt.Tx) error {
    ...
    return nil
})
您也可以在此闭包中获得数据库的一致视图，但是，在只读事务中不允许进行变异操作。您只能检索存储区，检索值，或者在只读事务中复制数据库。

#### 批量读写事务
每个 DB.Update() 等待磁盘提交写入。通过将多个更新与 DB.Batch() 函数结合，可以最小化这种开销：

err := db.Batch(func(tx *bolt.Tx) error {
    ...
    return nil
})
并发批量调用可以组合成更大的交易。 批处理仅在有多个 goroutine 调用时才有用。

如果部分事务失败，batch 可以多次调用给定的函数。 该函数必须是幂等的，只有在成功从 DB.Batch() 返回后才能生效。

例如：不要在函数内部显示消息，而是在封闭范围内设置变量：

var id uint64
err := db.Batch(func(tx *bolt.Tx) error {
    // Find last key in bucket, decode as bigendian uint64, increment
    // by one, encode back to []byte, and add new key.
    
    ...
    id = newValue
    return nil
})
if err != nil {
    return ...
}
fmt.Println("Allocated ID %d", id)
#### 手动管理交易
DB.View()和DB.Update()函数是DB.Begin()函数的包装器。 这些帮助函数将启动事务，执行一个函数，然后在返回错误时安全地关闭事务。 这是使用 Bolt 交易的推荐方式。

但是，有时您可能需要手动开始和结束交易。 您可以直接使用DB.Begin()函数，但请务必关闭事务。

// Start a writable transaction.
tx, err := db.Begin(true)
if err != nil {
    return err
}
defer tx.Rollback()

// Use the transaction...
_, err := tx.CreateBucket([]byte("MyBucket"))
if err != nil {
    return err
}

// Commit the transaction and check for error.
if err := tx.Commit(); err != nil {
    return err
}
DB.Begin()的第一个参数是一个布尔值，说明事务是否可写。

#### 使用 buckets
存储区是数据库中 key/value 对的集合。 bucket 中的所有 key 必须是唯一的。 您可以使用 DB.CreateBucket() 函数创建一个存储 bucket：

db.Update(func(tx *bolt.Tx) error {
    b, err := tx.CreateBucket([]byte("MyBucket"))
    if err != nil {
        return fmt.Errorf("create bucket: %s", err)
    }
    return nil
})
只有在使用 Tx.CreateBucketIfNotExists() 函数不存在的情况下，可以创建一个 bucket 。 在打开数据库之后，为所有顶级 bucket 调用此函数是一种常见模式，因此您可以保证它们存在以备将来事务处理。

要删除一个 bucket，只需调用 Tx.DeleteBucket() 函数即可。

#### 使用 key/value 对
要将 key/value 对保存到 bucket，请使用 Bucket.Put() 函数：

db.Update(func(tx *bolt.Tx) error {
    b := tx.Bucket([]byte("MyBucket"))
    err := b.Put([]byte("answer"), []byte("42"))
    return err
})
这将在 MyBucket 的 bucket 中将 “answer” key的值设置为“42”。 要检索这个value，我们可以使用 Bucket.Get() 函数：

db.View(func(tx *bolt.Tx) error {
    b := tx.Bucket([]byte("MyBucket"))
    v := b.Get([]byte("answer"))
    fmt.Printf("The answer is: %s\n", v)
    return nil
})
Get() 函数不会返回错误，因为它的操作保证可以正常工作（除非有某种系统失败）。 如果 key 存在，则它将返回其字节片段值。 如果不存在，则返回零。 请注意，您可以将零长度值设置为与不存在的键不同的键。

#### 使用 Bucket.Delete() 函数从 bucket 中删除一个 key。

请注意，从 Get() 返回的值仅在事务处于打开状态时有效。 如果您需要在事务外使用一个值，则必须使用 copy() 将其复制到另一个字节片段。

#### 自动增加 bucket 的数量
通过使用 NextSequence() 函数，您可以让 Bolt 确定一个可用作 key/value 对唯一标识符的序列。看下面的例子。

// CreateUser saves u to the store. The new user ID is set on u once the data is persisted.
func (s *Store) CreateUser(u *User) error {
    return s.db.Update(func(tx *bolt.Tx) error {
        // Retrieve the users bucket.
        // This should be created when the DB is first opened.
        
        b := tx.Bucket([]byte("users"))

        // Generate ID for the user.
        // This returns an error only if the Tx is closed or not writeable.
        // That can't happen in an Update() call so I ignore the error check.
        id, _ := b.NextSequence()
        u.ID = int(id)

        // Marshal user data into bytes.
        buf, err := json.Marshal(u)
        if err != nil {
            return err
        }

        // Persist bytes to users bucket.
        return b.Put(itob(u.ID), buf)
    })
}

// itob returns an 8-byte big endian representation of v.
func itob(v int) []byte {
    b := make([]byte, 8)
    binary.BigEndian.PutUint64(b, uint64(v))
    return b
}

type User struct {
    ID int
    ...
}
#### 迭代keys
Bolt 将 keys 以字节排序的顺序存储在一个 bucket 中。这使得对这些 keys 的顺序迭代非常快。要遍历 key，我们将使用一个 Cursor:

db.View(func(tx *bolt.Tx) error {
    // Assume bucket exists and has keys
    b := tx.Bucket([]byte("MyBucket"))

    c := b.Cursor()

    for k, v := c.First(); k != nil; k, v = c.Next() {
        fmt.Printf("key=%s, value=%s\n", k, v)
    }

    return nil
})

Cursor允许您移动到键列表中的特定点，并一次向前或向后移动一个键。
Cursor上有以下功能：
First()  Move to the first key.
Last()   Move to the last key.
Seek()   Move to a specific key.
Next()   Move to the next key.
Prev()   Move to the previous key.
每个函数都有（key [] byte，value [] byte）的返回签名。 当你迭代到游标的末尾时，Next() 将返回一个零键。 在调用 Next() 或 Prev() 之前，您必须使用 First()，Last() 或 Seek() 来寻找位置。 如果你不寻找位置，那么这些函数将返回一个零键。

在迭代期间，如果 key 非零，但是 value 为零，则意味着 key 指的是一个 bucket而不是一个 value。 使用 Bucket.Bucket() 访问子 bucket。

#### 前缀扫描
迭代关键字前缀，可以将 Seek() 和 bytes.HasPrefix() 组合起来：
db.View(func(tx *bolt.Tx) error {
    // Assume bucket exists and has keys
    c := tx.Bucket([]byte("MyBucket")).Cursor()

    prefix := []byte("1234")
    for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
        fmt.Printf("key=%s, value=%s\n", k, v)
    }

    return nil
})
#### 范围扫描
另一个常见的用例是扫描一个范围，如时间范围。如果您使用可排序的时间编码（如RFC3339），那么您可以查询特定的日期范围，如下所示：

db.View(func(tx *bolt.Tx) error {
    // Assume our events bucket exists and has RFC3339 encoded time keys.
    c := tx.Bucket([]byte("Events")).Cursor()

    // Our time range spans the 90's decade.
    min := []byte("1990-01-01T00:00:00Z")
    max := []byte("2000-01-01T00:00:00Z")

    // Iterate over the 90's.
    for k, v := c.Seek(min); k != nil && bytes.Compare(k, max) <= 0; k, v = c.Next() {
        fmt.Printf("%s: %s\n", k, v)
    }

    return nil
})
请注意，尽管RFC3339是可排序的，但RFC3339Nano的Golang实现不会在小数点后使用固定数量的数字，因此无法排序。

ForEach()
你也可以使用 ForEach() 函数，如果你知道你将迭代 bucket 中的所有 keys：
db.View(func(tx *bolt.Tx) error {
    // Assume bucket exists and has keys
    b := tx.Bucket([]byte("MyBucket"))

    b.ForEach(func(k, v []byte) error {
        fmt.Printf("key=%s, value=%s\n", k, v)
        return nil
    })
    return nil
})
请注意，ForEach()中的键和值仅在事务处于打开状态时有效。如果您需要使用事务外的键或值，则必须使用copy()将其复制到另一个字节片。

### 嵌套的 bucket
您也可以在一个 key 中存储一个 bucket 来创建嵌套的 bucket 。 API 与 DB 对象上的存储区管理 API 相同：
func (*Bucket) CreateBucket(key []byte) (*Bucket, error)
func (*Bucket) CreateBucketIfNotExists(key []byte) (*Bucket, error)
func (*Bucket) DeleteBucket(key []byte) error
假设您有一个多租户应用程序，其中根级 bucket 是帐户 bucket。 这个 bucket 里面是一系列的帐户，它们本身就是一个 bucket。 而在序列的 bucket 中，可以有许多与账户本身相关的存储区（用户，备注等）将信息分隔成逻辑分组。

// createUser creates a new user in the given account.
func createUser(accountID int, u *User) error {
    // Start the transaction.
    tx, err := db.Begin(true)
    if err != nil {
        return err
    }
    defer tx.Rollback()

    // Retrieve the root bucket for the account.
    // Assume this has already been created when the account was set up.
    root := tx.Bucket([]byte(strconv.FormatUint(accountID, 10)))

    // Setup the users bucket.
    bkt, err := root.CreateBucketIfNotExists([]byte("USERS"))
    if err != nil {
        return err
    }

    // Generate an ID for the new user.
    userID, err := bkt.NextSequence()
    if err != nil {
        return err
    }
    u.ID = userID

    // Marshal and save the encoded user.
    if buf, err := json.Marshal(u); err != nil {
        return err
    } else if err := bkt.Put([]byte(strconv.FormatUint(u.ID, 10)), buf); err != nil {
        return err
    }

    // Commit the transaction.
    if err := tx.Commit(); err != nil {
        return err
    }

    return nil
}

### 数据库备份
Blot 是一个单一的文件，所以很容易备份。 您可以使用Tx.WriteTo()函数将数据库的一致视图写入目的地。 如果您从只读事务中调用它，它将执行热备份而不会阻止其他数据库的读写操作。

默认情况下，它将使用一个常规的文件句柄来利用操作系统的页面缓存。 有关优化大于RAM数据集的信息，请参阅Tx文档。

一个常见的用例是通过HTTP进行备份，因此您可以使用像cURL这样的工具来进行数据库备份：

func BackupHandleFunc(w http.ResponseWriter, req *http.Request) {
    err := db.View(func(tx *bolt.Tx) error {
        w.Header().Set("Content-Type", "application/octet-stream")
        w.Header().Set("Content-Disposition", `attachment; filename="my.db"`)
        w.Header().Set("Content-Length", strconv.Itoa(int(tx.Size())))
        _, err := tx.WriteTo(w)
        return err
    })
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
    }
}
那么你可以用这个命令备份：

$ curl http://localhost/backup > my.db
或者你可以打开你的浏览器到http://localhost/backup，它会自动下载。
如果你想备份到另一个文件，你可以使用Tx.CopyFile()辅助函数。

### 与其他数据库比较
Postgres, MySQL, & other relational databases
关系数据库将数据结构化为行，并且只能通过使用SQL来访问。 这种方法提供了如何存储和查询数据的灵活性，但也会导致解析和规划SQL语句的开销。 Bolt通过字节切片键访问所有数据。 这使得 Bolt 可以快速读写数据，但不提供内置的连接值的支持。

大多数关系数据库（SQLite除外）都是独立于服务器的独立服务器。 这使您的系统可以灵活地将多个应用程序服务器连接到单个数据库服务器，但是也增加了通过网络序列化和传输数据的开销。 Bolt 作为应用程序中包含的库运行，因此所有数据访问都必须通过应用程序的进程。 这使数据更接近您的应用程序，但限制了多进程访问数据。

### LevelDB, RocksDB
LevelDB 及其衍生产品（RocksDB，HyperLevelDB）与 Bolt 类似，它们被绑定到应用程序中，但是它们的底层结构是日志结构合并树（LSM树）。 LSM 树通过使用提前写入日志和称为 SSTables 的多层排序文件来优化随机写入。 Bolt 在内部使用 B+ 树，只有一个文件。 两种方法都有折衷。

如果您需要高随机写入吞吐量（> 10,000 w / sec）或者您需要使用旋转磁盘，则 LevelDB可能是一个不错的选择。 如果你的应用程序是重读的，或者做了很多范围扫描，Bolt 可能是一个不错的选择。
 
另一个重要的考虑是 LevelDB 没有交易。 它支持批量写入键/值对，它支持读取快照，但不会使您能够安全地进行比较和交换操作。 Bolt 支持完全可序列化的 ACID 事务。

### LMDB
Bolt 最初是 LMDB 的一个类似实现，所以在结构上是相似的。 两者都使用 B+ 树，具有完全可序列化事务的 ACID 语义，并且使用单个writer 和多个 reader 来支持无锁 MVCC。

这两个项目有些分歧。 LMDB 主要关注原始性能，而 Bolt 专注于简单性和易用性。 例如，LMDB 允许执行一些不安全的操作，如直接写操作。 Bolt 选择禁止可能使数据库处于损坏状态的操作。 这个在 Bolt 中唯一的例外是 DB.NoSync。

API 也有一些差异。 打开 mdb_env 时 LMDB 需要最大的 mmap 大小，而Bolt将自动处理增量式 mmap 大小调整。 LMDB 用多个标志重载 getter 和 setter 函数，而 Bolt 则将这些特殊的情况分解成它们自己的函数。

### 注意事项和限制
选择正确的工具是非常重要的，Bolt 也不例外。在评估和使用 Bolt 时，需要注意以下几点：

Bolt 适合读取密集型工作负载。顺序写入性能也很快，但随机写入可能会很慢。您可以使用DB.Batch()或添加预写日志来帮助缓解此问题。
Bolt在内部使用B +树，所以可以有很多随机页面访问。与旋转磁盘相比，SSD可显着提高性能。
尽量避免长时间运行读取事务。 Bolt使用copy-on-write技术，旧的事务正在使用，旧的页面不能被回收。
从 Bolt 返回的字节切片只在交易期间有效。 一旦事务被提交或回滚，那么它们指向的内存可以被新页面重用，或者可以从虚拟内存中取消映射，并且在访问时会看到一个意外的故障地址恐慌。
Bolt在数据库文件上使用独占写入锁，因此不能被多个进程共享
使用Bucket.FillPercent时要小心。设置具有随机插入的 bucket 的高填充百分比会导致数据库的页面利用率很差。
一般使用较大的 bucket。较小的 bucket 会导致较差的页面利用率，一旦它们大于页面大小（通常为4KB）。
将大量批量随机写入加载到新存储区可能会很慢，因为页面在事务提交之前不会分裂。不建议在单个事务中将超过 100,000 个键/值对随机插入单个新 bucket
中。
Bolt使用内存映射文件，以便底层操作系统处理数据的缓存。 通常情况下，操作系统将缓存尽可能多的文件，并在需要时释放内存到其他进程。 这意味着Bolt在处理大型数据库时可以显示非常高的内存使用率。 但是，这是预期的，操作系统将根据需要释放内存。 Bolt可以处理比可用物理RAM大得多的数据库，只要它的内存映射适合进程虚拟地址空间。 这在32位系统上可能会有问题。
Bolt数据库中的数据结构是存储器映射的，所以数据文件将是endian特定的。 这意味着你不能将Bolt文件从一个小端机器复制到一个大端机器并使其工作。 对于大多数用户来说，这不是一个问题，因为大多数现代的CPU都是小端的。
由于页面在磁盘上的布局方式，Bolt无法截断数据文件并将空闲页面返回到磁盘。 相反，Bolt 在其数据文件中保留一个未使用页面的空闲列表。 这些免费页面可以被以后的交易重复使用。 由于数据库通常会增长，所以对于许多用例来说，这是很好的方法 但是，需要注意的是，删除大块数据不会让您回收磁盘上的空间。
