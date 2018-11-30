package bolt

import (
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"
	"hash/fnv"
	"strings"
	"runtime/debug"
	"sync"
	"log"
	"errors"
)

// mmap虚拟内存映射最大值1GB
// The largest step that can be taken when remapping the mmap.
const maxMmapStep = 1 << 30 // 1GB

// 数据文件格式版本
// The data file format version.
const version = 2

// 标记值表明文件是bolt db
// Represents a marker value to indicate that a file is a Bolt DB.
const magic uint32 = 0xED0CDAED

// 当同步数据改变到文件，是否忽略DB中NoSync字段
// Note: 仅限部分操作系统 比如OpenBSD; 没有统一缓存器缓存同时写必须同步调用msync的syscall
// IgnoreNoSync specifies whether the NoSync field of a DB is ignored when
// syncing changes to a file.  This is required as some operating systems,
// such as OpenBSD, do not have a unified buffer cache (UBC) and writes
// must be synchronized using the msync(2) syscall.
const IgnoreNoSync = runtime.GOOS == "openbsd"

// Default values if not set in a DB instance.
const (
	DefaultMaxBatchSize  int = 1000                   // 最大batch处理大小
	DefaultMaxBatchDelay     = 10 * time.Millisecond  // 最大batch处理延迟周期
	DefaultAllocSize         = 16 * 1024 * 1024       // 分配大小
)

// 默认操作系统的页大小
// default page size for db is set to the OS page size.
var defaultPageSize = os.Getpagesize()

/*
 * boltdb是以页为单位存储的， 并包含起始的两个meta页，一个或多个页来存储空闲页的页号以及剩下的分支页(bracnchPage)和叶子也(leafPage)
 * 在创建一个boltdb文件时 通过fwrite和fdatasync系统调用向磁盘写入32KB或16KB的初始化文件
 * 在打开一个boltdb文件时 使用mmap系统调用创建内存映射只读记录 用来读取文件中各页的数据
 */

// 一个bucket的集合并写入到磁盘上的文件中
// 所有的数据访问都需要通过事务来访问，通过对象DB可以获得transaction对象
// Note: 当DB的方法在Open方法之间被调用 则返回数据库未打开的错误[ErrDatabaseNotOpen]
// DB represents a collection of buckets persisted to a file on disk.
// All data access is performed through transactions which can be obtained through the DB.
// All the functions on DB will return a ErrDatabaseNotOpen if accessed before Open() is called.
type DB struct {
	// 当启用时，在每次执行commit之后都会进行一次Check()操作
	// 当数据库database处于不一致状态时 则会触发一个panic
	// 该操作会带来很大的性能损耗 需要谨慎使用（仅限目的用于调试）
	// When enabled, the database will perform a Check() after every commit.
	// A panic is issued if the database is in an inconsistent state. This
	// flag has a large performance impact so it should only be used for
	// debugging purposes.
	StrictMode bool

	// 设置NoSync会导致数据库database在每次执行commit()后跳过fsync()调用
	// 当批量加载到数据库database是比较有用的
	// 在系统故障或数据库database损耗的情况下，能够重启批量加载
	// 正常使用下 可以不用设置该变量
	// Note：一旦设置包级别全局变量IgnoreNoSync=true 则NoSync会被忽略
	//       该操作并不安全 使用时需要小心
	// Setting the NoSync flag will cause the database to skip fsync()
	// calls after each commit. This can be useful when bulk loading data
	// into a database and you can restart the bulk load in the event of
	// a system failure or database corruption. Do not set this flag for
	// normal use.
	//
	// If the package global IgnoreNoSync constant is true, this value is
	// ignored.  See the comment on that constant for more details.
	//
	// THIS IS UNSAFE. PLEASE USE WITH CAUTION.
	NoSync bool

	// 当设置为true时，在增长数据库时 会跳过所有的truncate()调用
	// 只有在非ext3/ext4系统上 将该字段设置为true是安全的
	// 跳过truncation可以避免硬盘空间预分配，并在重新映射的时候 跳过truncate()和fsync()
	// When true, skips the truncate call when growing the database.
	// Setting this to true is only safe on non-ext3/ext4 systems.
	// Skipping truncation avoids preallocation of hard drive space and
	// bypasses a truncate() and fsync() syscall on remapping.
	//
	// https://github.com/boltdb/bolt/issues/284
	NoGrowSync bool

	// 如果想要快速的读取整个数据库，可以通过设置MmapFlag = syscall.MAP_POPULATE
	// Note： 需要系统内核Linux 2.6.23+ 使用顺序预读取的功能
	// If you want to read the entire database fast, you can set MmapFlag to
	// syscall.MAP_POPULATE on Linux 2.6.23+ for sequential read-ahead.
	MmapFlags int

	// 设置一个批处理最大的内容大小
	// <= 0 则禁止批处理
	// Note:不要和Batch()同时改变
	// MaxBatchSize is the maximum size of a batch. Default value is
	// copied from DefaultMaxBatchSize in Open.
	//
	// If <=0, disables batching.
	//
	// Do not change concurrently with calls to Batch.
	MaxBatchSize int

	// 真正启动批处理之前的延迟时长
	// <= 禁用批处理
	// MaxBatchDelay is the maximum delay before a batch starts.
	// Default value is copied from DefaultMaxBatchDelay in Open.
	//
	// If <=0, effectively disables batching.
	//
	// Do not change concurrently with calls to Batch.
	MaxBatchDelay time.Duration

	// 当数据库database需要创建新的数据页时 需要分配空间大小
	// 这样做的目的主要是为了分摊数据库文件增长时 truncate() 和 fsync()带来的成本
	// AllocSize is the amount of space allocated when the database
	// needs to create new pages. This is done to amortize the cost
	// of truncate() and fsync() when growing the data file.
	AllocSize int

	path     string                                     		// 数据库地址
	file     *os.File											// 数据库文件
	lockfile *os.File // windows only						    // 文件锁 （仅限windows系统）
	dataref  []byte   // mmap'ed readonly, write throws SEGV    // 虚拟映射空间(仅限于只读，写操作会触发SEGV)
	data     *[maxMapSize]byte                                  // 数据
	datasz   int												// 数据大小
	filesz   int // current on disk file size					// 当前磁盘的文件大小
	meta0    *meta												// 元数据
	meta1    *meta                                              // 元数据
	pageSize int											    // 数据页大小
	opened   bool                                               // 数据库状态
	rwtx     *Tx                                                // 读写事务
	txs      []*Tx                                              // 只读事务
	freelist *freelist                                          //
	stats    Stats                                              // 状态

	pagePool sync.Pool                                          // 数据页池

	batchMu sync.Mutex
	batch   *batch                                              // 批处理

	rwlock   sync.Mutex   // Allows only one writer at a time.  // 读写事务(一次仅有一个)
	metalock sync.Mutex   // Protects meta page access.         // 元页访问锁
	mmaplock sync.RWMutex // Protects mmap access during remapping. // 在重新映射期间 mmap的访问
	statlock sync.RWMutex // Protects stats access.                 // 数据库状态锁

	ops struct {                                                   // 写数据
		writeAt func(b []byte, off int64) (n int, err error)
	}

	// 只读模式： 当设置为true时 则不能执行Update操作和设定Begin(true) 会抛出ErrDatabaseReadOnly异常
	// Read only mode.
	// When true, Update() and Begin(true) return ErrDatabaseReadOnly immediately.
	readOnly bool
}

// 当前打开数据库的路径
// Path returns the path to currently open database file.
func (db *DB) Path() string {
	return db.path
}

// 返回数据库字符串的表示形式
// GoString returns the Go string representation of the database.
func (db *DB) GoString() string {
	return fmt.Sprintf("bolt.DB{path:%q}", db.path)
}

// String returns the string representation of the database.
func (db *DB) String() string {
	return fmt.Sprintf("DB<%q>", db.path)
}

// 打开一个给定路径的数据库database
// 若是指定的path不存在，则会主动创建数据库
// 通过将Options设置为nil，则会导致bolt db使用默认的参数项
// Open creates and opens a database at the given path.
// If the file does not exist then it will be created automatically.
// Passing in nil options will cause Bolt to open the database with the default options.
func Open(path string,  // 数据库本地地址
	mode os.FileMode, 	// 文件模式
	options *Options) (*DB, error) { // 数据库参数(设置=nil 可使用默认参数值)
 	var db = &DB{opened: true}

	// Set default options if no options are provided.
	if options == nil {  // 使用默认配置数据库配置参数
		options = DefaultOptions
	}
	db.NoGrowSync = options.NoGrowSync
	db.MmapFlags = options.MmapFlags

	// Set default values for later DB operations.
	db.MaxBatchSize = DefaultMaxBatchSize
	db.MaxBatchDelay = DefaultMaxBatchDelay
	db.AllocSize = DefaultAllocSize

	flag := os.O_RDWR    // 系统读写模式
	if options.ReadOnly {  // 若是只读事务操作 只需只读权限
		flag = os.O_RDONLY
		db.readOnly = true
	}

	// Open data file and separate sync handler for metadata writes.
	db.path = path
	var err error
	if db.file, err = os.OpenFile(db.path, flag|os.O_CREATE, mode); err != nil {   //打开指定数据库文件
		_ = db.close()
		return nil, err
	}

	// 当Bolt DB处于读写模式通过使用锁文件保证在同一次操作中其他的进程不能够使用数据库database
	// 当两个进程能够各自写meta page和释放空闲page 这会产生糟糕的结果
	// 当处于非只读模式，数据库文件被锁定 仅有一个进程能够获得这把锁
	// 通过使用共享锁来锁定数据库文件(不止一个进程可能会获得这把锁在同一次) 或者通过设定数据库配置参数options.ReadOnly=true，开启只读模式
	// 为了防止进入不确定的获取锁的不确定操作 指定Timeout参数来规避：获取文件锁的超时时间
	// Lock file so that other processes using Bolt in read-write mode cannot
	// use the database  at the same time. This would cause corruption since
	// the two processes would write meta pages and free pages separately.
	// The database file is locked exclusively (only one process can grab the lock)
	// if !options.ReadOnly.
	// The database file is locked using the shared lock (more than one process may
	// hold a lock at the same time) otherwise (options.ReadOnly is set).
	if err := flock(db, mode, !db.readOnly, options.Timeout); err != nil {
		_ = db.close()
		return nil, err
	}

	// 数据库操作
	// Default values for test hooks
	db.ops.writeAt = db.file.WriteAt

	// 若是数据库不存在 则进行初始化
	// Initialize the database if it doesn't exist.
	if info, err := db.file.Stat(); err != nil {
		return nil, err
	} else if info.Size() == 0 {  // 数据文件大小 = 0 则需要进行meta data file初始化
		// Initialize new files with meta pages.
		if err := db.init(); err != nil {
			return nil, err
		}
	} else {
		// 在数据库已完成初始化的情况下
		// 读取第一页的meta data数据
		// Read the first meta page to determine the page size.
		var buf [0x1000]byte  // 4k大小buffer
		if _, err := db.file.ReadAt(buf[:], 0); err == nil {
			m := db.pageInBuffer(buf[:], 0).meta()  // 读取第一页的meta数据
			if err := m.validate(); err != nil {   // 验证
				// 获取系统的页大小来填充boltdb的page size
				// Note: 若是第一页是无效的并且操作系统使用不同的page size来创建数据库的那么会导致我们不能访问数据库database
				// 因此任何针对meta数据改变 再覆盖都会导致对应的数据库不可访问
				// If we can't read the page size, we can assume it's the same
				// as the OS -- since that's how the page size was chosen in the
				// first place.
				//
				// If the first page is invalid and this OS uses a different
				// page size than what the database was created with then we
				// are out of luck and cannot access the database.
				db.pageSize = os.Getpagesize()
			} else {
				db.pageSize = int(m.pageSize)
			}
		}
	}

	// 初始化数据页池
	// Initialize page pool.
	db.pagePool = sync.Pool{
		New: func() interface{} {
			return make([]byte, db.pageSize)
		},
	}

	// 内存映射缓存数据文件 增强数据访问速度
	// Memory map the data file.
	if err := db.mmap(options.InitialMmapSize); err != nil {
		_ = db.close()
		return nil, err
	}

	// 读取freelist
	// Read in the freelist.
	db.freelist = newFreelist()
	db.freelist.read(db.page(db.meta().freelist))

	// 标记数据库已打开 并返回db对象 能够进行read和write操作
	// Mark the database as opened and return.
	return db, nil
}

// 打开底层内存映射文件并初始化元引用
// 参数：minsz 新的mmap最小的尺寸
// mmap opens the underlying memory-mapped file and initializes the meta references.
// minsz is the minimum size that the new mmap can be.
func (db *DB) mmap(minsz int) error {
	// 拿到mmap锁
	db.mmaplock.Lock()
	defer db.mmaplock.Unlock()

	// 数据库文件信息
	// 进行内存映射文件 需要当前的boltdb的大小 超过数据库page size的2倍以上大小 否则不需要进行映射
	info, err := db.file.Stat()
	if err != nil {
		return fmt.Errorf("mmap stat error: %s", err)
	} else if int(info.Size()) < db.pageSize*2 {
		return fmt.Errorf("file size too small")
	}

	// 确保size是最小的尺寸 保证最终确定的映射文件的大小是OK的
	// Ensure the size is at least the minimum size.
	var size = int(info.Size())
	if size < minsz {
		size = minsz
	}
	// 确定mmamp映射文件的大小（在mmap系统调用时指定映射文件的起始偏移量以及偏移的长度 即可确定映射文件的范围）
	size, err = db.mmapSize(size)
	if err != nil {
		return err
	}

	// 引用所有的mmap的引用(保证对应的映射是可用的)
	// Dereference all mmap references before unmapping.
	if db.rwtx != nil {
		db.rwtx.root.dereference()
	}

	// 对于老数据要先进行取消映射的操作
	// Unmap existing data before continuing.
	if err := db.munmap(); err != nil {
		return err
	}

	// 将数据库文件以字节序列的方式映射到内存中
	// Memory-map the data file as a byte slice.
	if err := mmap(db, size); err != nil {
		return err
	}

	// 数据库文件中前2页时meta数据页 前面已对数据进行mmap内存映射 需要进行验证数据库是否正常可用
	// Save references to the meta pages.
	db.meta0 = db.page(0).meta()
	db.meta1 = db.page(1).meta()

	// 验证第一页、第二页的meta：若是两页都验证失败会返回一个error
	// 当meta0验证失败 那么我们可以通过meta1来进行恢复 反之亦然。
	// Validate the meta pages. We only return an error if both meta pages fail
	// validation, since meta0 failing validation means that it wasn't saved
	// properly -- but we can recover using meta1. And vice-versa.
	err0 := db.meta0.validate()
	err1 := db.meta1.validate()
	if err0 != nil && err1 != nil {
		return err0
	}

	return nil
}

// 取消数据库文件与内存间的映射(一般在进行数据mmap映射时 已存在映射的老数据需要进行这一步操作)
// munmap unmaps the data file from memory.
func (db *DB) munmap() error {
	if err := munmap(db); err != nil {
		return fmt.Errorf("unmap error: " + err.Error())
	}
	return nil
}

// 确定适当的mmap大小 根据给定的数据库尺寸，最小是32KB，以双倍的增长：32KB、64KB、128KB...直至达到1GB，
// 当对应的mmap大小超过1GB后 接下来的增长每次都以1GB来增长 直至达到maxMapSize
// 一旦mmap的大小超过允许的最大映射尺寸 则返回一个error
// mmapSize determines the appropriate size for the mmap given the current size
// of the database. The minimum size is 32KB and doubles until it reaches 1GB.
// Returns an error if the new mmap size is greater than the max allowed.
func (db *DB) mmapSize(size int) (int, error) {
	// Double the size from 32KB until 1GB.
	// 当mmap大小未超过1GB时 从32KB 以双倍增长
	for i := uint(15); i <= 30; i++ {
		if size <= 1<<i {
			return 1 << i, nil
		}
	}

	// 是否已超过mmap所分配最大尺寸
	// Verify the requested size is not above the maximum allowed.
	if size > maxMapSize {
		return 0, fmt.Errorf("mmap too large")
	}

	// 已超过1GB 则以每次增长1GB继续增长 直至达到maxMapSize
	// If larger than 1GB then grow by 1GB at a time.
	sz := int64(size)
	if remainder := sz % int64(maxMmapStep); remainder > 0 {
		sz += int64(maxMmapStep) - remainder
	}

	// 确保mmap的大小是page size的整数倍 便于数据读取
	// Ensure that the mmap size is a multiple of the page size.
	// This should always be true since we're incrementing in MBs.
	pageSize := int64(db.pageSize)
	if (sz % pageSize) != 0 {
		sz = ((sz / pageSize) + 1) * pageSize
	}

	// If we've exceeded the max size then only grow up to the max size.
	if sz > maxMapSize {
		sz = maxMapSize
	}

	return int(sz), nil
}

// 创建新的数据库(database)文件并初始化元数据页
// 通过init来初始化一个空的数据库database,对应的boltdb的数据库文件的基本格式：是以页为基本单位，一个数据库文件由若干个页组成(页的大小由操作系统的pagesize来决定)
// 初始化的数据库文件默认情况下是4页，其中前2页用来存 meta data 第三页存放freelist的页面  第四页存放K/V的页面
// freelist和K/V数据存放的页面 并不限于第三页和第四页
// init creates a new database file and initializes its meta pages.
func (db *DB) init() error {
	// 根据系统的页尺寸设定数据库的pagesize
	// 数据读取一般都是按页读取
	// Set the page size to the OS page size.
	db.pageSize = os.Getpagesize()

	// 创建一个buffer来容纳两个meta page
	// Create two meta pages on a buffer.
	buf := make([]byte, db.pageSize*4)
	// 前两页存放meta data
	for i := 0; i < 2; i++ {
		p := db.pageInBuffer(buf[:], pgid(i))
		p.id = pgid(i)
		p.flags = metaPageFlag

		// Initialize the meta page.
		m := p.meta()
		m.magic = magic
		m.version = version
		m.pageSize = uint32(db.pageSize)
		m.freelist = 2
		m.root = bucket{root: 3}
		m.pgid = 4
		m.txid = txid(i)
		m.checksum = m.sum64()
	}

	// 存放freelist空白页记录
	// Write an empty freelist at page 3.
	p := db.pageInBuffer(buf[:], pgid(2))
	p.id = pgid(2)
	p.flags = freelistPageFlag
	p.count = 0

	// 存放K/V记录页
	// Write an empty leaf page at page 4.
	p = db.pageInBuffer(buf[:], pgid(3))
	p.id = pgid(3)
	p.flags = leafPageFlag
	p.count = 0

	// 将buffer写入到boltdb的数据库文件
	// Write the buffer to our data file.
	if _, err := db.ops.writeAt(buf, 0); err != nil {
		return err
	}
	// 将buffer中的数据通过fdatasync调用内核的磁盘页缓存进行刷盘
	if err := fdatasync(db); err != nil {
		return err
	}

	return nil
}

// 释放数据库相关的资源
// 在数据库关闭之前 所有的事务操作必须先关闭,不管read-write事务 还是read-only事务
// Close releases all database resources.
// All transactions must be closed before closing the database.
func (db *DB) Close() error {
	db.rwlock.Lock()                 // 读写锁
	defer db.rwlock.Unlock()

	db.metalock.Lock()              // 元数据锁
	defer db.metalock.Unlock()

	db.mmaplock.RLock()            // mmap内存映射锁
	defer db.mmaplock.RUnlock()

	return db.close()
}

// 数据库关闭
func (db *DB) close() error {
	if !db.opened {
		return nil
	}
	// 释放原有分配内容
	db.opened = false

	db.freelist = nil

	// Clear ops.
	db.ops.writeAt = nil

	// Close the mmap.
	if err := db.munmap(); err != nil {
		return err
	}

	// Close file handles.
	if db.file != nil {
		// No need to unlock read-only file.
		if !db.readOnly {  // 非只读事务模式下 数据库被单一的进程独占 需要释放对应的文件锁
			// Unlock the file.
			if err := funlock(db); err != nil {
				log.Printf("bolt.Close(): funlock error: %s", err)
			}
		}
		// 关于对应的数据库文件的描述符
		// Close the file descriptor.
		if err := db.file.Close(); err != nil {
			return fmt.Errorf("db file close: %s", err)
		}
		db.file = nil
	}

	db.path = ""
	return nil
}

// 开启事务
// 只读事务能够在并发一次使用多个; 而写事务仅能一次使用一个
// 一旦开启多个写事务会触发锁机制直到当前的写事务完成 后续的其他写事务才能继续
// 事务不应该相互依赖，比如在一个goroutine里同时打开一个只读事务和读写事务将导致读写事务出现死锁 主要由于数据库database存在变化 需要定期的重新映射data file
// 但是在只读事务情况 将不会出现该情况
// Note: 当长时间的读事务执行 需要调整设置DB.InitialMmapSize值足够大以确保写事务存在的潜在阻塞
//       当使用完一个只读事务 需要关闭该事务，否则旧页数据将不能被清理
// Begin starts a new transaction.
// Multiple read-only transactions can be used concurrently but only one
// write transaction can be used at a time. Starting multiple write transactions
// will cause the calls to block and be serialized until the current write
// transaction finishes.
//
// Transactions should not be dependent on one another. Opening a read
// transaction and a write transaction in the same goroutine can cause the
// writer to deadlock because the database periodically needs to re-mmap itself
// as it grows and it cannot do that while a read transaction is open.
//
// If a long running read transaction (for example, a snapshot transaction) is
// needed, you might want to set DB.InitialMmapSize to a large enough value
// to avoid potential blocking of write transaction.
//
// IMPORTANT: You must close read-only transactions after you are finished or
// else the database will not reclaim old pages.
func (db *DB) Begin(writable bool) (*Tx, error) {
	if writable {                   // 当前数据操作 属于写  不同的操作采用的事务是不一样 对应的底层实现原理也不一致。
		return db.beginRWTx()      // 开启读写事务
	}
	return db.beginTx()				// 开启只读事务
}

// 开启只读事务
func (db *DB) beginTx() (*Tx, error) {
	// 当初始化事务时 需要锁定meta page
	// 获取meta的锁在mmap锁之前 为了防止写事务获取了meta
	// Lock the meta pages while we initialize the transaction. We obtain
	// the meta lock before the mmap lock because that's the order that the
	// write transaction will obtain them.
	db.metalock.Lock()

	// 获取mmap的只读锁
	// 当mmap重新进行内存数据映射时 会获取到一把写锁 这也就导致所有的事务必须在该写锁能够完成映射之前完成各自的事务
	// Obtain a read-only lock on the mmap. When the mmap is remapped it will
	// obtain a write lock so all transactions must finish before it can be
	// remapped.
	db.mmaplock.RLock()

	// 数据库是否已打开
	// Exit if the database is not open yet.
	if !db.opened {
		db.mmaplock.RUnlock()
		db.metalock.Unlock()
		return nil, ErrDatabaseNotOpen
	}

	// 新建一个事务并与数据库database进行关联 完成数据库初始化
	// Create a transaction associated with the database.
	t := &Tx{}
	t.init(db)

	// 跟踪数据库开启的事务 直到其关闭
	// Keep track of transaction until it closes.
	db.txs = append(db.txs, t)
	n := len(db.txs)

	// 一旦数据库database完成初始化之后 meta page不需要锁定了
	// Unlock the meta pages.
	db.metalock.Unlock()

	// 更新数据库database的统计信息
	// Update the transaction stats.
	db.statlock.Lock()
	db.stats.TxN++       	// 事务个数
	db.stats.OpenTxN = n  	// 已打开的事务
	db.statlock.Unlock()

	return t, nil          // 返回数据库database关联的事务  接下来可以基于该事务进行读写操作
}

// 读写事务
func (db *DB) beginRWTx() (*Tx, error) {
	// 读写事务必须当前数据库database模式是read-write模式
	// If the database was opened with Options.ReadOnly, return an error.
	if db.readOnly {
		return nil, ErrDatabaseReadOnly
	}

	// 获取数据库的读写锁 直至对应的事务关闭该锁将被释放
	// 这也导致了当开启一个写事务时  一次只能运行一个； 直至该事务结束 其他的写事务才有机会操作
	// Obtain writer lock. This is released by the transaction when it closes.
	// This enforces only one writer transaction at a time.
	db.rwlock.Lock()

	// 一旦获取到写锁之后 需要锁定meta 页 这样接下来我们才能完成读写事务的操作
	// Once we have the writer lock then we can lock the meta pages so that
	// we can set up the transaction.
	db.metalock.Lock()
	defer db.metalock.Unlock()

	// 一切事务的操作都必须保证当前数据库database是开启的
	// Exit if the database is not open yet.
	if !db.opened {
		db.rwlock.Unlock()
		return nil, ErrDatabaseNotOpen
	}

	// 创建读写事务并关联数据库database完成初始化
	// Create a transaction associated with the database.
	t := &Tx{writable: true}
	t.init(db)
	db.rwtx = t

	// 释放一些已关闭的只读事务对应的页
	// Free any pages associated with closed read-only transactions.
	var minid txid = 0xFFFFFFFFFFFFFFFF
	for _, t := range db.txs {  // 遍历当前存活的事务 提取最小的事务id
		if t.meta.txid < minid {
			minid = t.meta.txid
		}
	}
	if minid > 0 {    // 小于最小活跃事务的其他事务 均属于已关闭的事务 可能还占有对应的page
		db.freelist.release(minid - 1)
	}

	return t, nil   // 返回对应的读写事务
}

// 移除数据库事务
// removeTx removes a transaction from the database.
func (db *DB) removeTx(tx *Tx) {
	// 释放mmap的读锁 便于读事务的读取操作
	// Release the read lock on the mmap.
	db.mmaplock.RUnlock()

	// meta需要锁定
	// Use the meta lock to restrict access to the DB object.
	db.metalock.Lock()

	// Remove the transaction.
	for i, t := range db.txs {  // 遍历需要需要删除的事务
		if t == tx {
			last := len(db.txs) - 1
			db.txs[i] = db.txs[last] // 填充已被删除的事务空位置的内容  使用最后一个事务填充 减少数据移动
			db.txs[last] = nil       // 置空最后一个位置的事务
			db.txs = db.txs[:last]
			break
		}
	}
	n := len(db.txs)

	// Unlock the meta pages.
	db.metalock.Unlock()

	// 合并统计数据
	// Merge statistics.
	db.statlock.Lock()
	db.stats.OpenTxN = n
	db.stats.TxStats.add(&tx.stats)
	db.statlock.Unlock()
}

// Update是在读写托管事务上下文中执行一个函数。
// 若是没有error产生 则事务提交; 否则将会执行回滚, 不论是在函数执行过程中还是事务提交过程中。
// 在函数内部手动提交或回滚事务都将触发一个panic
// Update executes a function within the context of a read-write managed transaction.
// If no error is returned from the function then the transaction is committed.
// If an error is returned then the entire transaction is rolled back.
// Any error that is returned from the function or returned from the commit is
// returned from the Update() method.
//
// Attempting to manually commit or rollback within the function will cause a panic.
func (db *DB) Update(fn func(*Tx) error) error {
	// 首先开启一个读写事务
	t, err := db.Begin(true)
	if err != nil {
		return err
	}

	// 要确保不论是程序执行过程中还是执行commit提交时出现panic都能够完成对应的事务回滚
	// Make sure the transaction rolls back in the event of a panic.
	defer func() {
		if t.db != nil {
			t.rollback()
		}
	}()

	// 将创建的事务transaction设置为托管事务 防止内部程序手动提交commit
	// 一旦手动提交commit 则会产生一个panic
	// Mark as a managed tx so that the inner function cannot manually commit.
	t.managed = true

	// 一旦内部方法返回error 则会进行事务回滚rollback并返回error
	// If an error is returned from the function then rollback and return error.
	err = fn(t)
	t.managed = false // 取消事务托管
	if err != nil {
		_ = t.Rollback()
		return err
	}

	return t.Commit()
}

// 在托管只读事务的上下文内部执行一个函数
// 不论返回任何error 都将从View方法返回
// 与update操作类型 不能在内部函数执行rollback 会导致一个panic
// View executes a function within the context of a managed read-only transaction.
// Any error that is returned from the function is returned from the View() method.
//
// Attempting to manually rollback within the function will cause a panic.
func (db *DB) View(fn func(*Tx) error) error {
	// 开启只读事务
	t, err := db.Begin(false)
	if err != nil {
		return err
	}

	// 确保在出现panic时 对应的事务能够完成回滚
	// Make sure the transaction rolls back in the event of a panic.
	defer func() {
		if t.db != nil {
			t.rollback()
		}
	}()

	// 防止手动rollback 将事务进行托管
	// Mark as a managed tx so that the inner function cannot manually rollback.
	t.managed = true

	// 内部函数执行过程产生了error 则将error返回
	// If an error is returned from the function then pass it through.
	err = fn(t)
	t.managed = false
	if err != nil {
		_ = t.Rollback()
		return err
	}

	if err := t.Rollback(); err != nil {
		return err
	}

	return nil
}

// Batch操作也调用一个内部函数fn，该内部函数fn也算是batch的一部分
// 特殊情况(除外)
// 1、并发执行batch则会进行合并到一个单独的Bolt事务中
// 2、内部函数传递给Batch则会多次调用，不论是否返回error
// 对于Batch操作来说其边界影响比较是幂等性的，并必须调用者获得执行成功的结果，对应的改变才会永久有效的
// 通过DB.MaxBatchSize和DB.MaxBatchDelay来调整batch的最大批大小和延迟时长
// Batch操作仅用于多协程调用时 有用的

// Batch calls fn as part of a batch. It behaves similar to Update,
// except:
//
// 1. concurrent Batch calls can be combined into a single Bolt
// transaction.
//
// 2. the function passed to Batch may be called multiple times,
// regardless of whether it returns error or not.
//
// This means that Batch function side effects must be idempotent and
// take permanent effect only after a successful return is seen in
// caller.
//
// The maximum batch size and delay can be adjusted with DB.MaxBatchSize
// and DB.MaxBatchDelay, respectively.
//
// Batch is only useful when there are multiple goroutines calling it.
func (db *DB) Batch(fn func(*Tx) error) error {
	// error信号
	errCh := make(chan error, 1)
	// 一次处理一个批次
	db.batchMu.Lock()
	// 批次没内容 或 已超过DB设定的批次尺寸最大值： 均需要新建一个batch 并绑定timer 等待delay时间后触发提交
	if (db.batch == nil) || (db.batch != nil && len(db.batch.calls) >= db.MaxBatchSize) {
		// There is no existing batch, or the existing batch is full; start a new one.
		db.batch = &batch{
			db: db,
		}
		db.batch.timer = time.AfterFunc(db.MaxBatchDelay, db.batch.trigger)
	}
	db.batch.calls = append(db.batch.calls, call{fn: fn, err: errCh})  // batch调用
	if len(db.batch.calls) >= db.MaxBatchSize {
		// wake up batch, it's ready to run
		go db.batch.trigger()
	}
	db.batchMu.Unlock()

	err := <-errCh
	if err == trySolo {   //
		err = db.Update(fn)
	}
	return err
}

// 调用
type call struct {
	fn  func(*Tx) error
	err chan<- error
}

// 批操作
type batch struct {
	db    *DB
	timer *time.Timer
	start sync.Once      // 用于保证batch操作仅执行一次
	calls []call
}

// 触发执行batch操作
// trigger runs the batch if it hasn't already been run.
func (b *batch) trigger() {
	b.start.Do(b.run)
}

// 在batch中执行事务并将结果回递给DB.Batch
// run performs the transactions in the batch and communicates results
// back to DB.Batch.
func (b *batch) run() {
	b.db.batchMu.Lock()
	b.timer.Stop()   // 开始运行batch时的 对应的timer触发器需要停用 以便后续的batch使用
	// Make sure no new work is added to this batch, but don't break
	// other batches.
	if b.db.batch == b {  // 确保执行的batch没有被改变
		b.db.batch = nil
	}
	b.db.batchMu.Unlock()

retry:
	for len(b.calls) > 0 {        // 循环指定batch的调用call
		var failIdx = -1
		err := b.db.Update(func(tx *Tx) error {          // 调用update来完成batch操作  所有的调用合并一个事务中
			for i, c := range b.calls {
				if err := safelyCall(c.fn, tx); err != nil {    // 调用内部函数fn和对应的事务完成操作
					failIdx = i                                 // 记录失败调用call
					return err
				}
			}
			return nil
		})

		if failIdx >= 0 {  // 剔除执行失败的事务
			// take the failing transaction out of the batch. it's
			// safe to shorten b.calls here because db.batch no longer
			// points to us, and we hold the mutex anyway.
			c := b.calls[failIdx]
			b.calls[failIdx], b.calls = b.calls[len(b.calls)-1], b.calls[:len(b.calls)-1]  // 剔除失败的那个调用call 以便后面的重执行
			// tell the submitter re-run it solo, continue with the rest of the batch
			// 继续剩余事务的执行 并将失败的调用call单独执行
			c.err <- trySolo
			continue retry   // 重新执行batch
		}

		// 将结果传回给调用者 不论失败还是成功
		// pass success, or bolt internal errors, to all callers
		for _, c := range b.calls {
			c.err <- err
		}
		break retry
	}
}

// trySolo is a special sentinel error value used for signaling that a
// transaction function should be re-run. It should never be seen by
// callers.
var trySolo = errors.New("batch function returned an error and should be re-run solo")

type panicked struct {
	reason interface{}
}

func (p panicked) Error() string {
	if err, ok := p.reason.(error); ok {
		return err.Error()
	}
	return fmt.Sprintf("panic: %v", p.reason)
}

// 执行事务  并保证事务安全执行
func safelyCall(fn func(*Tx) error, tx *Tx) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicked{p}
		}
	}()
	return fn(tx)
}

// 通过系统调用fdatasync来完成数据库文件的处理
// 虽然NoSync使用并不是必须的正常操作，一旦使用NoSync 则必须强制数据库文件与磁盘同步
// Sync executes fdatasync() against the database file handle.
//
// This is not necessary under normal operation, however, if you use NoSync
// then it allows you to force the database file to sync against the disk.
func (db *DB) Sync() error { return fdatasync(db) }

// 检索数据库的持续性能统计数据，仅在事务关闭时才更新。
// Stats retrieves ongoing performance stats for the database.
// This is only updated when a transaction closes.
func (db *DB) Stats() Stats {
	db.statlock.RLock()
	defer db.statlock.RUnlock()
	return db.stats
}

// This is for internal access to the raw data bytes from the C cursor, use
// carefully, or not at all.
func (db *DB) Info() *Info {
	return &Info{uintptr(unsafe.Pointer(&db.data[0])), db.pageSize}
}

// page retrieves a page reference from the mmap based on the current page size.
func (db *DB) page(id pgid) *page {
	pos := id * pgid(db.pageSize)
	return (*page)(unsafe.Pointer(&db.data[pos]))
}

// pageInBuffer retrieves a page reference from a given byte array based on the current page size.
func (db *DB) pageInBuffer(b []byte, id pgid) *page {
	return (*page)(unsafe.Pointer(&b[id*pgid(db.pageSize)]))
}

// meta retrieves the current meta page reference.
func (db *DB) meta() *meta {
	// We have to return the meta with the highest txid which doesn't fail
	// validation. Otherwise, we can cause errors when in fact the database is
	// in a consistent state. metaA is the one with the higher txid.
	metaA := db.meta0
	metaB := db.meta1
	if db.meta1.txid > db.meta0.txid {
		metaA = db.meta1
		metaB = db.meta0
	}

	// Use higher meta page if valid. Otherwise fallback to previous, if valid.
	if err := metaA.validate(); err == nil {
		return metaA
	} else if err := metaB.validate(); err == nil {
		return metaB
	}

	// This should never be reached, because both meta1 and meta0 were validated
	// on mmap() and we do fsync() on every write.
	panic("bolt.DB.meta(): invalid meta pages")
}

// allocate returns a contiguous block of memory starting at a given page.
func (db *DB) allocate(count int) (*page, error) {
	// Allocate a temporary buffer for the page.
	var buf []byte
	if count == 1 {
		buf = db.pagePool.Get().([]byte)
	} else {
		buf = make([]byte, count*db.pageSize)
	}
	p := (*page)(unsafe.Pointer(&buf[0]))
	p.overflow = uint32(count - 1)

	// Use pages from the freelist if they are available.
	if p.id = db.freelist.allocate(count); p.id != 0 {
		return p, nil
	}

	// Resize mmap() if we're at the end.
	p.id = db.rwtx.meta.pgid
	var minsz = int((p.id+pgid(count))+1) * db.pageSize
	if minsz >= db.datasz {
		if err := db.mmap(minsz); err != nil {
			return nil, fmt.Errorf("mmap allocate error: %s", err)
		}
	}

	// Move the page id high water mark.
	db.rwtx.meta.pgid += pgid(count)

	return p, nil
}

// grow grows the size of the database to the given sz.
func (db *DB) grow(sz int) error {
	// Ignore if the new size is less than available file size.
	if sz <= db.filesz {
		return nil
	}

	// If the data is smaller than the alloc size then only allocate what's needed.
	// Once it goes over the allocation size then allocate in chunks.
	if db.datasz < db.AllocSize {
		sz = db.datasz
	} else {
		sz += db.AllocSize
	}

	// Truncate and fsync to ensure file size metadata is flushed.
	// https://github.com/boltdb/bolt/issues/284
	if !db.NoGrowSync && !db.readOnly {
		if runtime.GOOS != "windows" {
			if err := db.file.Truncate(int64(sz)); err != nil {
				return fmt.Errorf("file resize error: %s", err)
			}
		}
		if err := db.file.Sync(); err != nil {
			return fmt.Errorf("file sync error: %s", err)
		}
	}

	db.filesz = sz
	return nil
}

func (db *DB) IsReadOnly() bool {
	return db.readOnly
}

// Options represents the options that can be set when opening a database.
type Options struct {
	// Timeout is the amount of time to wait to obtain a file lock.
	// When set to zero it will wait indefinitely. This option is only
	// available on Darwin and Linux.
	Timeout time.Duration

	// Sets the DB.NoGrowSync flag before memory mapping the file.
	NoGrowSync bool

	// Open database in read-only mode. Uses flock(..., LOCK_SH |LOCK_NB) to
	// grab a shared lock (UNIX).
	ReadOnly bool

	// Sets the DB.MmapFlags flag before memory mapping the file.
	MmapFlags int

	// InitialMmapSize is the initial mmap size of the database
	// in bytes. Read transactions won't block write transaction
	// if the InitialMmapSize is large enough to hold database mmap
	// size. (See DB.Begin for more information)
	//
	// If <=0, the initial map size is 0.
	// If initialMmapSize is smaller than the previous database size,
	// it takes no effect.
	InitialMmapSize int
}

// DefaultOptions represent the options used if nil options are passed into Open().
// No timeout is used which will cause Bolt to wait indefinitely for a lock.
var DefaultOptions = &Options{
	Timeout:    0,
	NoGrowSync: false,
}

// Stats represents statistics about the database.
type Stats struct {
	// Freelist stats
	FreePageN     int // total number of free pages on the freelist
	PendingPageN  int // total number of pending pages on the freelist
	FreeAlloc     int // total bytes allocated in free pages
	FreelistInuse int // total bytes used by the freelist

	// Transaction stats
	TxN     int // total number of started read transactions
	OpenTxN int // number of currently open read transactions

	TxStats TxStats // global, ongoing stats.
}

// Sub calculates and returns the difference between two sets of database stats.
// This is useful when obtaining stats at two different points and time and
// you need the performance counters that occurred within that time span.
func (s *Stats) Sub(other *Stats) Stats {
	if other == nil {
		return *s
	}
	var diff Stats
	diff.FreePageN = s.FreePageN
	diff.PendingPageN = s.PendingPageN
	diff.FreeAlloc = s.FreeAlloc
	diff.FreelistInuse = s.FreelistInuse
	diff.TxN = s.TxN - other.TxN
	diff.TxStats = s.TxStats.Sub(&other.TxStats)
	return diff
}

func (s *Stats) add(other *Stats) {
	s.TxStats.add(&other.TxStats)
}

type Info struct {
	Data     uintptr
	PageSize int
}

/*
     |--------header--------|
     |    id(0/1)      		|		页号
     |     0x40        		|		flags类型
     |     0           		|		存储元素数
     |     0           		|		是否有后续页
     |-------- ptr ---------|  		页头结束
     |    0xED0CDAED    	|		魔数(固定)
     |    2             	|		版本号
     |    0x1000        	|		页大小（4kb）
	 |    flags         	|		保留字段
     |    root          	|		根bucket的头信息
     |    freelist      	|		存放空闲页面的页号
     |    pgid          	|		总页数
     |    txid           	|		上一次事务id
     |    checksum       	|		哈希校验(排除该字段其他的字段)
     |    0x00           	|		meta记录结束
     | -------- end --------|
 */
// 元数据页
type meta struct {
	magic    uint32		// 魔数； 0xED0CDAED
	version  uint32		// boltdb文件格式的版本号 = 2
	pageSize uint32     // boltdb文件的页大小
	flags    uint32     // 保留字段
	root     bucket		// boltdb根bucket的头信息
	freelist pgid		// boltdb文件中存在的freelist的页号，用来存放空闲页面的页号
	pgid     pgid		// boltdb文件中总的页数
	txid     txid		// 上一次写数据库database的事务id  可作为当前boltdb的修改版本号，执行读写事务时+1， 只读事务不变
	checksum uint64		// 其他字段的64位FNV-1哈希校验
}

// 验证boltdb 的meta数据是否有效 主要根据magic 、version、checksum是否改变来作为依据
// validate checks the marker bytes and version of the meta page to ensure it matches this binary.
func (m *meta) validate() error {
	if m.magic != magic {
		return ErrInvalid
	} else if m.version != version {
		return ErrVersionMismatch
	} else if m.checksum != 0 && m.checksum != m.sum64() {
		return ErrChecksum
	}
	return nil
}

// copy copies one meta object to another.
func (m *meta) copy(dest *meta) {
	*dest = *m
}

// 将meta写入页
// write writes the meta onto a page.
func (m *meta) write(p *page) {
	// 参数检查
	if m.root.root >= m.pgid {
		panic(fmt.Sprintf("root bucket pgid (%d) above high water mark (%d)", m.root.root, m.pgid))
	} else if m.freelist >= m.pgid {
		panic(fmt.Sprintf("freelist pgid (%d) above high water mark (%d)", m.freelist, m.pgid))
	}

	// 由于数据库(database)的meta数据是存在前两页的 故而对应的页号是0或1
	// 通过事务ID来确定当前的meta的所在的页号
	// Note：由于txid每次在写数据库时+1 这样就保证了meta的页交替被更新
	// Page id is either going to be 0 or 1 which we can determine by the transaction ID.
	p.id = pgid(m.txid % 2)
	p.flags |= metaPageFlag     // 设置flag = metaPageFlag

	// 计算meta的数据校验和 便于后面验证meta数据
	// Calculate the checksum.
	m.checksum = m.sum64()
    // 将meta信息拷贝到页面P中的缓存相应位置
	m.copy(p.meta())
}

// meta数据的校验和
// generates the checksum for the meta.
func (m *meta) sum64() uint64 {
	var h = fnv.New64a()
	//
	_, _ = h.Write((*[unsafe.Offsetof(meta{}.checksum)]byte)(unsafe.Pointer(m))[:])
	return h.Sum64()
}

// _assert will panic with a given formatted message if the given condition is false.
func _assert(condition bool, msg string, v ...interface{}) {
	if !condition {
		panic(fmt.Sprintf("assertion failed: "+msg, v...))
	}
}

func warn(v ...interface{})              { fmt.Fprintln(os.Stderr, v...) }
func warnf(msg string, v ...interface{}) { fmt.Fprintf(os.Stderr, msg+"\n", v...) }

func printstack() {
	stack := strings.Join(strings.Split(string(debug.Stack()), "\n")[2:], "\n")
	fmt.Fprintln(os.Stderr, stack)
}
