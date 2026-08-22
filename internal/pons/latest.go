package pons

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// LaunchTokenMeta is the full metadata of one factory launch, recovered from
// the launch transaction's calldata (the TokenLaunched event itself does not
// carry name/logo/description).
type LaunchTokenMeta struct {
	Token       common.Address
	Curve       common.Address
	Deployer    common.Address
	Block       uint64
	Name        string
	Symbol      string
	Logo        string
	Description string
	Socials     V2Socials
}

// LatestLaunchMeta scans backwards from the chain head for the newest
// TokenLaunched event on the v2 factory and decodes its launch metadata. It
// gives up after maxBlocks blocks.
func (c *Client) LatestLaunchMeta(ctx context.Context, maxBlocks uint64) (LaunchTokenMeta, error) {
	head, err := c.eth.BlockNumber(ctx)
	if err != nil {
		return LaunchTokenMeta{}, fmt.Errorf("read head block: %w", err)
	}
	const step = 10_000
	to := head
	scanned := uint64(0)
	for {
		var from uint64
		if to >= step-1 {
			from = to - (step - 1)
		}
		logs, err := c.eth.FilterLogs(ctx, ethereum.FilterQuery{
			Addresses: []common.Address{c.factory},
			Topics:    [][]common.Hash{{tokenLaunchedTopic}},
			FromBlock: new(big.Int).SetUint64(from),
			ToBlock:   new(big.Int).SetUint64(to),
		})
		if err != nil {
			return LaunchTokenMeta{}, fmt.Errorf("scan factory logs %d-%d: %w", from, to, err)
		}
		for i := len(logs) - 1; i >= 0; i-- {
			launch, ok := decodeLaunch(logs[i])
			if !ok {
				continue
			}
			meta := LaunchTokenMeta{
				Token: launch.Token, Curve: launch.Curve,
				Deployer: launch.Deployer, Block: logs[i].BlockNumber,
			}
			if tx, _, err := c.eth.TransactionByHash(ctx, logs[i].TxHash); err == nil && tx != nil {
				fillLaunchMetaFromCalldata(&meta, tx.Data())
			}
			// ERC-20 fallback keeps name/symbol usable even when the calldata
			// cannot be decoded (e.g. a factory proxy or older signature).
			if meta.Name == "" {
				_ = c.callView(ctx, launch.Token, &erc20ABI, &meta.Name, "name")
			}
			if meta.Symbol == "" {
				_ = c.callView(ctx, launch.Token, &erc20ABI, &meta.Symbol, "symbol")
			}
			return meta, nil
		}
		scanned += to - from + 1
		if from == 0 || scanned >= maxBlocks {
			return LaunchTokenMeta{}, fmt.Errorf("no TokenLaunched event found within the last %d blocks", scanned)
		}
		to = from - 1
	}
}

// fillLaunchMetaFromCalldata decodes a launchToken(...) input and copies the
// metadata fields into meta. Returns false when the calldata does not match.
func fillLaunchMetaFromCalldata(meta *LaunchTokenMeta, data []byte) bool {
	method, ok := factoryABI.Methods["launchToken"]
	if !ok || len(data) < 4 || !bytes.Equal(data[:4], method.ID) {
		return false
	}
	values, err := method.Inputs.Unpack(data[4:])
	if err != nil || len(values) == 0 {
		return false
	}
	params, ok := abi.ConvertType(values[0], new(V2TokenParams)).(*V2TokenParams)
	if !ok || params == nil {
		return false
	}
	meta.Name, meta.Symbol = params.Name, params.Symbol
	meta.Logo, meta.Description = params.Logo, params.Description
	meta.Socials = params.Socials
	return true
}
