package pons

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestBuildRouterBuyEncoding(t *testing.T) {
	key, _ := crypto.GenerateKey()
	s := &Signer{key: key, addr: crypto.PubkeyToAddress(key.PublicKey), chainID: big.NewInt(RobinhoodChainID)}
	router := common.HexToAddress(MMRouter)
	curve := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	spend := big.NewInt(123456789)
	tx, err := s.BuildRouterBuy(router, curve, 53_000_003, spend, TxParams{Nonce: 7, GasLimit: 400_000, TipCap: big.NewInt(1), FeeCap: big.NewInt(2)})
	if err != nil {
		t.Fatal(err)
	}
	if *tx.To() != router {
		t.Fatalf("to = %s, want router", tx.To().Hex())
	}
	if tx.Value().Cmp(spend) != 0 {
		t.Fatalf("value = %s, want %s", tx.Value(), spend)
	}
	method := mmRouterABI.Methods["buyWithin"]
	if !bytes.Equal(tx.Data()[:4], method.ID) {
		t.Fatalf("selector mismatch")
	}
	args, err := method.Inputs.Unpack(tx.Data()[4:])
	if err != nil {
		t.Fatal(err)
	}
	if args[0].(common.Address) != curve {
		t.Fatalf("curve arg = %v", args[0])
	}
	if args[1].(*big.Int).Uint64() != 53_000_003 {
		t.Fatalf("maxL2Block arg = %v", args[1])
	}
	if args[2].(common.Address) != s.addr {
		t.Fatalf("recipient arg = %v, want signer", args[2])
	}
	from, err := types.Sender(types.LatestSignerForChainID(s.chainID), tx)
	if err != nil || from != s.addr {
		t.Fatalf("sender = %s err=%v", from.Hex(), err)
	}
}

func TestRoutedBuyFromReceipt(t *testing.T) {
	router := common.HexToAddress(MMRouter)
	caller := common.HexToAddress("0x0000000000000000000000000000000000000a11")
	curve := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	recipient := caller
	ev := mmRouterABI.Events["RoutedBuy"]
	data, err := ev.Inputs.NonIndexed().Pack(big.NewInt(1000), big.NewInt(5_000_000), big.NewInt(25), big.NewInt(53_000_002), big.NewInt(53_000_003))
	if err != nil {
		t.Fatal(err)
	}
	rcpt := &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{{
		Address: router,
		Topics:  []common.Hash{ev.ID, common.BytesToHash(caller.Bytes()), common.BytesToHash(curve.Bytes()), common.BytesToHash(recipient.Bytes())},
		Data:    data,
	}}}
	got, ok := RoutedBuyFromReceipt(rcpt, router, recipient)
	if !ok {
		t.Fatal("RoutedBuy not decoded")
	}
	if got.Curve != curve || got.Caller != caller || got.QuoteIn.Int64() != 1000 || got.TokensOut.Int64() != 5_000_000 || got.Refunded.Int64() != 25 || got.L2Block != 53_000_002 || got.MaxL2Block != 53_000_003 {
		t.Fatalf("decoded %+v", got)
	}
	if _, ok := RoutedBuyFromReceipt(rcpt, router, common.HexToAddress("0x1")); ok {
		t.Fatal("decoded a RoutedBuy for the wrong recipient")
	}
	if _, ok := RoutedBuyFromReceipt(&types.Receipt{Status: types.ReceiptStatusFailed}, router, recipient); ok {
		t.Fatal("decoded a RoutedBuy from a reverted receipt")
	}
}

func TestDecodeRouterRevertExpired(t *testing.T) {
	e := mmRouterABI.Errors["Expired"]
	args, err := e.Inputs.Pack(big.NewInt(53_000_009), big.NewInt(53_000_003))
	if err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte{}, e.ID[:4]...), args...)
	err = DecodeRouterRevert(data)
	exp, ok := err.(*ExpiredError)
	if !ok {
		t.Fatalf("got %T %v, want *ExpiredError", err, err)
	}
	if exp.Current != 53_000_009 || exp.Max != 53_000_003 {
		t.Fatalf("decoded %+v", exp)
	}
	if DecodeRouterRevert([]byte{0x08, 0xc3, 0x79, 0xa0}) != nil {
		t.Fatal("Error(string) selector must not decode as a router error")
	}
}

// fakeNode answers eth_chainId, eth_blockNumber and batched
// eth_sendRawTransaction so the tracker's polling path and the batch sender
// can be exercised without a chain.
type fakeNode struct {
	height   atomic.Uint64
	rawSeen  atomic.Int64
	rejectAt int // 1-based index within a batch to reject; 0 = accept all
}

func (f *fakeNode) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqs []map[string]any
		batch := true
		if err := json.Unmarshal(body, &reqs); err != nil {
			var single map[string]any
			if err := json.Unmarshal(body, &single); err != nil {
				http.Error(w, "bad json", 400)
				return
			}
			reqs, batch = []map[string]any{single}, false
		}
		resps := make([]map[string]any, 0, len(reqs))
		for i, req := range reqs {
			resp := map[string]any{"jsonrpc": "2.0", "id": req["id"]}
			switch req["method"] {
			case "eth_chainId":
				resp["result"] = "0x1237"
			case "eth_blockNumber":
				resp["result"] = "0x" + big.NewInt(int64(f.height.Load())).Text(16)
			case "eth_sendRawTransaction":
				f.rawSeen.Add(1)
				if f.rejectAt == i+1 {
					resp["error"] = map[string]any{"code": -32000, "message": "nonce too low"}
				} else {
					resp["result"] = common.HexToHash("0xabc").Hex()
				}
			default:
				resp["error"] = map[string]any{"code": -32601, "message": "unsupported"}
			}
			resps = append(resps, resp)
		}
		w.Header().Set("Content-Type", "application/json")
		if batch {
			json.NewEncoder(w).Encode(resps)
		} else {
			json.NewEncoder(w).Encode(resps[0])
		}
	}
}

func TestSendRawBatchMapsPerElementErrors(t *testing.T) {
	node := &fakeNode{rejectAt: 2}
	srv := httptest.NewServer(node.handler())
	defer srv.Close()
	ctx := context.Background()
	c, err := Dial(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.GenerateKey()
	s := &Signer{key: key, addr: crypto.PubkeyToAddress(key.PublicKey), chainID: big.NewInt(0x1237)}
	var txs []*types.Transaction
	for i := 0; i < 3; i++ {
		tx, err := s.BuildTransfer(common.HexToAddress("0x1"), big.NewInt(1), TxParams{Nonce: uint64(i), GasLimit: 21000, TipCap: big.NewInt(1), FeeCap: big.NewInt(2)})
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	errs := c.SendRawBatch(ctx, txs)
	if node.rawSeen.Load() != 3 {
		t.Fatalf("node saw %d raw txs, want 3 in one batch", node.rawSeen.Load())
	}
	if errs[0] != nil || errs[2] != nil {
		t.Fatalf("accepted elements reported errors: %v %v", errs[0], errs[2])
	}
	if errs[1] == nil {
		t.Fatal("rejected element reported no error")
	}
}
