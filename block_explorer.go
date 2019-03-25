package blockexplorer

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/badger"
	"github.com/fletaio/common/util"
	"github.com/fletaio/core/block"
	"github.com/fletaio/core/kernel"
	"github.com/labstack/echo"
)

var (
	libPath string
)

func init() {
	var pwd string
	{
		pc := make([]uintptr, 10) // at least 1 entry needed
		runtime.Callers(1, pc)
		f := runtime.FuncForPC(pc[0])
		pwd, _ = f.FileLine(pc[0])

		path := strings.Split(pwd, "/")
		pwd = strings.Join(path[:len(path)-1], "/")
	}

	libPath = pwd
}

//Block explorer error list
var (
	ErrDbNotClear          = errors.New("Db is not clear")
	ErrNotEnoughParameter  = errors.New("Not enough parameter")
	ErrNotTransactionHash  = errors.New("This hash is not a transaction hash")
	ErrNotBlockHash        = errors.New("This hash is not a block hash")
	ErrInvalidHeightFormat = errors.New("Invalid height format")
)

// BlockExplorer struct
type BlockExplorer struct {
	Kernel                 *kernel.Kernel
	transactionCountList   []*countInfo
	CurrentChainInfo       currentChainInfo
	lastestTransactionList []txInfos

	db *badger.DB

	e          *echo.Echo
	webChecker echo.MiddlewareFunc
}

type countInfo struct {
	Time  int64 `json:"time"`
	Count int   `json:"count"`
}

//NewBlockExplorer TODO
func NewBlockExplorer(dbPath string, Kernel *kernel.Kernel) (*BlockExplorer, error) {
	os.Remove(dbPath) //TODO REMOVE THIS CODE
	opts := badger.DefaultOptions
	opts.Dir = dbPath
	opts.ValueDir = dbPath
	opts.Truncate = true
	opts.SyncWrites = true
	lockfilePath := filepath.Join(opts.Dir, "LOCK")
	os.MkdirAll(dbPath, os.ModeDir)

	os.Remove(lockfilePath)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	{
	again:
		if err := db.RunValueLogGC(0.7); err != nil {
		} else {
			goto again
		}
	}

	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		for range ticker.C {
		again:
			if err := db.RunValueLogGC(0.7); err != nil {
			} else {
				goto again
			}
		}
	}()

	e := &BlockExplorer{
		Kernel:                 Kernel,
		transactionCountList:   []*countInfo{},
		lastestTransactionList: []txInfos{},
		db: db,
	}
	e.initURL()

	if err := e.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(blockChainInfoBytes)
		if err != nil {
			if err != badger.ErrKeyNotFound {
				return err
			}
		} else {
			value, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			buf := bytes.NewBuffer(value)
			e.CurrentChainInfo.ReadFrom(buf)
		}

		return nil
	}); err != nil {
		return nil, ErrDbNotClear
	}

	currHeight := e.Kernel.Provider().Height()

	for i := currHeight; i > 0; i-- {
		if len(e.lastestTransactionList) >= 500 {
			break
		}
		b, err := e.Kernel.Block(i)
		if err != nil {
			continue
		}
		txs := b.Body.Transactions
		for _, tx := range txs {
			name, _ := e.Kernel.Transactor().NameByType(tx.Type())
			e.lastestTransactionList = append(e.lastestTransactionList, txInfos{
				TxHash:    tx.Hash().String(),
				BlockHash: b.Header.Hash().String(),
				ChainID:   b.Header.ChainCoord.String(),
				Time:      tx.Timestamp(),
				TxType:    name,
			})
		}
	}

	go func(e *BlockExplorer) {
		for {
			time.Sleep(time.Second)
			e.updateChainInfoCount()
		}
	}(e)

	return e, nil
}

var blockChainInfoBytes = []byte("blockChainInfo")

func (e *BlockExplorer) LastestTransactionLen() int {
	return len(e.lastestTransactionList)
}
func (e *BlockExplorer) updateChainInfoCount() error {
	currHeight := e.Kernel.Provider().Height()

	e.CurrentChainInfo.currentTransactions = 0
	e.CurrentChainInfo.Foumulators = e.Kernel.CandidateCount()
	minHeight := e.CurrentChainInfo.Blocks
	e.CurrentChainInfo.Blocks = currHeight

	newTxs := []txInfos{}
	newTxCountInfos := []*countInfo{}
	for i := currHeight - currHeight%2; i > minHeight && i >= 0; i -= 2 {
		height := i
		b, err := e.Kernel.Block(height)
		if err != nil {
			continue
		}
		height2 := i - 1
		b2, err := e.Kernel.Block(height2)
		if err != nil {
			continue
		}
		e.CurrentChainInfo.currentTransactions += len(b.Body.Transactions)
		e.CurrentChainInfo.currentTransactions += len(b2.Body.Transactions)

		if len(newTxCountInfos) < 200 {
			newTxCountInfos = append(newTxCountInfos, &countInfo{
				Time:  int64(b.Header.Timestamp()),
				Count: len(b.Body.Transactions) + len(b2.Body.Transactions),
			})
		}

		txs := b.Body.Transactions
		for _, tx := range txs {
			name, _ := e.Kernel.Transactor().NameByType(tx.Type())
			if len(newTxs) > 500 {
				break
			}
			newTxs = append(newTxs, txInfos{
				TxHash:    tx.Hash().String(),
				BlockHash: b.Header.Hash().String(),
				ChainID:   b.Header.ChainCoord.String(),
				Time:      tx.Timestamp(),
				TxType:    name,
			})
		}

		txs = b2.Body.Transactions
		for _, tx := range txs {
			name, _ := e.Kernel.Transactor().NameByType(tx.Type())
			if len(newTxs) > 500 {
				break
			}
			newTxs = append(newTxs, txInfos{
				TxHash:    tx.Hash().String(),
				BlockHash: b.Header.Hash().String(),
				ChainID:   b.Header.ChainCoord.String(),
				Time:      tx.Timestamp(),
				TxType:    name,
			})
		}

		e.updateBlock(b, height)
		e.updateBlock(b2, height2)
	}
	e.CurrentChainInfo.Blocks = currHeight

	if len(newTxs) > 0 {
		e.lastestTransactionList = append(newTxs, e.lastestTransactionList...)
		if len(e.lastestTransactionList) > 500 {
			e.lastestTransactionList = e.lastestTransactionList[:500]
		}
	}
	if len(newTxCountInfos) > 0 {
		e.transactionCountList = append(newTxCountInfos, e.transactionCountList...)
		if len(e.transactionCountList) > 500 {
			e.transactionCountList = e.transactionCountList[:500]
		}
	}

	e.CurrentChainInfo.Transactions += e.CurrentChainInfo.currentTransactions

	if err := e.db.Update(func(txn *badger.Txn) error {
		buf := &bytes.Buffer{}
		_, err := e.CurrentChainInfo.WriteTo(buf)
		if err != nil {
			return err
		}
		txn.Set(blockChainInfoBytes, buf.Bytes())
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (e *BlockExplorer) updateBlock(b *block.Block, height uint32) error {
	if err := e.db.Update(func(txn *badger.Txn) error {
		//start block hash update
		err := e.updateHashs(txn, height)
		if err != nil {
			return err
		}
		//end block hash update
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (e *BlockExplorer) updateHashs(txn *badger.Txn, height uint32) error {
	b, err := e.Kernel.Block(height)
	if err != nil {
		return err
	}
	value := util.Uint32ToBytes(height)

	h := b.Header.Hash().String()
	if err := txn.Set([]byte(h), value); err != nil {
		return err
	}

	formulatorAddr := []byte("formulator" + b.Header.Formulator.String())
	item, err := txn.Get(formulatorAddr)
	if err != nil {
		if err != badger.ErrKeyNotFound {
			return err
		}
		txn.Set(formulatorAddr, util.Uint32ToBytes(1))
	} else {
		value, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		height := util.BytesToUint32(value)
		txn.Set(formulatorAddr, util.Uint32ToBytes(height+1))
	}

	txs := b.Body.Transactions
	for i, tx := range txs {
		h := tx.Hash()
		v := append(value, util.Uint32ToBytes(uint32(i))...)
		if err := txn.Set(h[:], v); err != nil {
			return err
		}
	}
	return nil
}

func (e *BlockExplorer) GetBlockCount(formulatorAddr string) (height uint32) {
	formulatorKey := []byte("formulator" + formulatorAddr)
	e.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(formulatorKey)
		if err != nil {
			if err != badger.ErrKeyNotFound {
				return err
			}
			height = 0
		} else {
			value, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			height = util.BytesToUint32(value)
		}

		return nil
	})
	return
}

func (e *BlockExplorer) initURL() {
	e.e = echo.New()
	web := NewWebServer(e.e, "./webfiles")
	e.e.Renderer = web

	ec := NewExplorerController(e.db, e)

	fs := http.FileServer(Assets)
	e.e.GET("/resource/*", echo.WrapHandler(fs))

	e.webChecker = func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			web.CheckWatch()
			return next(c)
		}
	}

	e.e.Any("/data/:order", e.dataHandler)
	// http.HandleFunc("/data/", e.dataHandler)
	e.e.GET("/", func(c echo.Context) error {
		args := make(map[string]interface{})
		err := c.Render(http.StatusOK, "index.html", args)
		if err != nil {
			log.Println(err)
		}
		return err
	}, e.webChecker)
	e.e.GET("/blocks", func(c echo.Context) error {
		args, err := ec.Blocks(c.Request())
		if err != nil {
			log.Println(err)
		}
		err = c.Render(http.StatusOK, "blocks.html", args)
		if err != nil {
			log.Println(err)
		}
		return err
	}, e.webChecker)
	e.e.GET("/blockDetail", func(c echo.Context) error {
		args, err := ec.BlockDetail(c.Request())
		if err != nil {
			log.Println(err)
		}
		err = c.Render(http.StatusOK, "blockDetail.html", args)
		if err != nil {
			log.Println(err)
		}
		return err
	}, e.webChecker)
	e.e.GET("/transactions", func(c echo.Context) error {
		args, err := ec.Transactions(c.Request())
		if err != nil {
			log.Println(err)
		}
		err = c.Render(http.StatusOK, "transactions.html", args)
		if err != nil {
			log.Println(err)
		}
		return err
	}, e.webChecker)
	e.e.GET("/transactionDetail", func(c echo.Context) error {
		args, err := ec.TransactionDetail(c.Request())
		if err != nil {
			log.Println(err)
		}
		err = c.Render(http.StatusOK, "transactionDetail.html", args)
		if err != nil {
			log.Println(err)
		}
		return err
	}, e.webChecker)

}

// StartExplorer is start web server
func (e *BlockExplorer) StartExplorer(port int) {
	e.e.Start(":" + strconv.Itoa(port))
}

// func (e *BlockExplorer) dataHandler(w http.ResponseWriter, r *http.Request) {
func (e *BlockExplorer) dataHandler(c echo.Context) error {
	order := c.Param("order")
	var result interface{}

	switch order {
	case "transactions.data":
		result = e.transactions()
	case "currentChainInfo.data":
		result = e.CurrentChainInfo
	case "lastestBlocks.data":
		result = e.lastestBlocks()
	case "lastestTransactions.data":
		result = e.lastestTransactions()
	case "paginationBlocks.data":
		startStr := c.QueryParam("start")
		result = e.paginationBlocks(startStr)
	case "paginationTxs.data":
		startStr := c.QueryParam("start")
		result = e.paginationTxs(startStr)
	}
	return c.JSON(http.StatusOK, result)
}
