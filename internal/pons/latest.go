package pons

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"unicode/utf8"

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

// fillLaunchMetaFromCalldata recovers the V2TokenParams tuple from the launch
// transaction input and copies the metadata fields into meta. It first decodes
// a direct factory launchToken call; when the launch went through a wrapper
// (e.g. the official router that bundles an atomic initial buy under its own
// selector), it scans the calldata head for an embedded params tuple instead,
// so the exact wrapper signature does not need to be known.
func fillLaunchMetaFromCalldata(meta *LaunchTokenMeta, data []byte) bool {
	method, ok := factoryABI.Methods["launchToken"]
	if !ok || len(data) < 4 {
		return false
	}
	if bytes.Equal(data[:4], method.ID) {
		if values, err := method.Inputs.Unpack(data[4:]); err == nil && len(values) > 0 {
			if params, ok := abi.ConvertType(values[0], new(V2TokenParams)).(*V2TokenParams); ok && applyLaunchParams(meta, params) {
				return true
			}
		}
	}
	return scanForLaunchParams(meta, data[4:], method.Inputs[0].Type)
}

// scanForLaunchParams treats every word in the calldata head as a candidate
// offset to a V2TokenParams tuple and returns the first that decodes to sane
// metadata. ABI dynamic-tuple offsets are relative to the tuple start, so a
// tuple embedded by any wrapper method decodes identically.
func scanForLaunchParams(meta *LaunchTokenMeta, body []byte, paramsType abi.Type) bool {
	args := abi.Arguments{{Type: paramsType}}
	head := len(body)
	if head > 32*32 {
		head = 32 * 32
	}
	for pos := 0; pos+32 <= head; pos += 32 {
		offset := new(big.Int).SetBytes(body[pos : pos+32])
		if !offset.IsInt64() {
			continue
		}
		o := offset.Int64()
		// A params tuple head alone is 10 words (320 bytes).
		if o < 32 || o%32 != 0 || o+320 > int64(len(body)) {
			continue
		}
		// Prefix a 0x20 offset word so the tuple is decoded in place.
		buf := make([]byte, 32+len(body)-int(o))
		buf[31] = 0x20
		copy(buf[32:], body[o:])
		values, err := args.Unpack(buf)
		if err != nil || len(values) == 0 {
			continue
		}
		params, ok := abi.ConvertType(values[0], new(V2TokenParams)).(*V2TokenParams)
		if ok && applyLaunchParams(meta, params) {
			return true
		}
	}
	return false
}

func applyLaunchParams(meta *LaunchTokenMeta, params *V2TokenParams) bool {
	if params == nil || !plausibleText(params.Name, 96) || !plausibleText(params.Symbol, 32) {
		return false
	}
	meta.Name, meta.Symbol = params.Name, params.Symbol
	meta.Logo, meta.Description = params.Logo, params.Description
	meta.Socials = params.Socials
	return true
}

// plausibleText guards the offset scan against garbage that happens to decode:
// real token names and symbols are short, non-empty, valid UTF-8 and contain
// no control bytes.
func plausibleText(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
