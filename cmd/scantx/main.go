// Command scantx finds recent transactions sent by one address by scanning a
// block window located via timestamp binary search.
package main

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

func main() {
	sender := common.HexToAddress(os.Args[1])
	fromTS, _ := strconv.ParseInt(os.Args[2], 10, 64)
	toTS, _ := strconv.ParseInt(os.Args[3], 10, 64)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	eth, err := ethclient.DialContext(ctx, "https://rpc.mainnet.chain.robinhood.com")
	must(err)
	head, err := eth.BlockNumber(ctx)
	must(err)

	blockAt := func(ts int64) uint64 {
		lo, hi := uint64(0), head
		for lo < hi {
			mid := (lo + hi) / 2
			h, err := eth.HeaderByNumber(ctx, new(big.Int).SetUint64(mid))
			must(err)
			if int64(h.Time) < ts {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo
	}
	start, end := blockAt(fromTS), blockAt(toTS)
	fmt.Printf("scanning blocks %d..%d (%d blocks)\n", start, end, end-start+1)

	signer := types.LatestSignerForChainID(big.NewInt(4663))
	found := 0
	for n := start; n <= end; n++ {
		block, err := eth.BlockByNumber(ctx, new(big.Int).SetUint64(n))
		if err != nil {
			continue
		}
		for _, tx := range block.Transactions() {
			from, err := types.Sender(signer, tx)
			if err != nil || from != sender {
				continue
			}
			found++
			rcpt, err := eth.TransactionReceipt(ctx, tx.Hash())
			status := "receipt-error"
			var gasUsed uint64
			if err == nil {
				status = map[uint64]string{0: "REVERTED", 1: "ok"}[rcpt.Status]
				gasUsed = rcpt.GasUsed
			}
			to := "contract-creation"
			if tx.To() != nil {
				to = tx.To().Hex()
			}
			sel := ""
			if len(tx.Data()) >= 4 {
				sel = common.Bytes2Hex(tx.Data()[:4])
			}
			fmt.Printf("block=%d nonce=%d tx=%s to=%s selector=%s value=%s status=%s gasUsed=%d\n",
				n, tx.Nonce(), tx.Hash().Hex(), to, sel, tx.Value().String(), status, gasUsed)
		}
	}
	fmt.Printf("done, %d txs from %s\n", found, sender.Hex())
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
