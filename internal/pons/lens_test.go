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
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func TestBalanceLensABIRoundTrip(t *testing.T) {
	addrs := []common.Address{common.HexToAddress("0x1"), common.HexToAddress("0x2")}
	native := []*big.Int{big.NewInt(1e18), big.NewInt(2e18)}
	tokens := []*big.Int{big.NewInt(11), big.NewInt(22)}

	encoded, err := balanceLensABI.Methods["snapshot"].Outputs.Pack(native, tokens)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := balanceLensABI.Unpack("snapshot", encoded)
	if err != nil {
		t.Fatal(err)
	}
	gotN := vals[0].([]*big.Int)
	gotT := vals[1].([]*big.Int)
	if gotN[0].Cmp(native[0]) != 0 || gotT[1].Cmp(tokens[1]) != 0 {
		t.Fatalf("round-trip snapshot = %v / %v", gotN, gotT)
	}

	data, err := balanceLensABI.Pack("ethBalances", addrs)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 4 {
		t.Fatalf("packed calldata too short: %d", len(data))
	}
}

func TestEthBalancesOneCall(t *testing.T) {
	addrs := []common.Address{common.HexToAddress("0x11"), common.HexToAddress("0x22")}
	want := []*big.Int{big.NewInt(100), big.NewInt(200)}
	payload, err := balanceLensABI.Methods["ethBalances"].Outputs.Pack(want)
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, batch := decodeJSONRPC(t, body)
		if batch {
			t.Errorf("eth_call should be a single request, got batch: %s", body)
			return
		}
		switch req["method"] {
		case "eth_chainId":
			writeJSONRPC(w, req["id"], "0x1237")
		case "eth_call":
			calls.Add(1)
			writeJSONRPC(w, req["id"], hexutil.Encode(payload))
		default:
			t.Errorf("unexpected method %v", req["method"])
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c, err := Dial(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	got, err := c.EthBalances(ctx, addrs)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("eth_call count = %d, want 1", calls.Load())
	}
	if len(got) != 2 || got[0].Cmp(want[0]) != 0 || got[1].Cmp(want[1]) != 0 {
		t.Fatalf("balances = %v, want %v", got, want)
	}
}

func TestPendingNoncesAreSingleCalls(t *testing.T) {
	addrs := []common.Address{
		common.BigToAddress(big.NewInt(1)),
		common.BigToAddress(big.NewInt(2)),
		common.BigToAddress(big.NewInt(3)),
	}
	var noncePosts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, batch := decodeJSONRPC(t, body)
		if batch {
			t.Errorf("nonce refresh must not use JSON-RPC batches: %s", body)
			return
		}
		switch req["method"] {
		case "eth_chainId":
			writeJSONRPC(w, req["id"], "0x1237")
		case "eth_getTransactionCount":
			n := noncePosts.Add(1)
			writeJSONRPC(w, req["id"], hexutil.EncodeUint64(uint64(n)))
		default:
			t.Errorf("unexpected method %v", req["method"])
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c, err := Dial(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	got, err := c.PendingNonces(ctx, addrs)
	if err != nil {
		t.Fatal(err)
	}
	if noncePosts.Load() != 3 {
		t.Fatalf("nonce RPCs = %d, want 3", noncePosts.Load())
	}
	if got[0] != 1 || got[2] != 3 {
		t.Fatalf("nonces = %v", got)
	}
}

func TestEthBalancesEmpty(t *testing.T) {
	c := &Client{}
	got, err := c.EthBalances(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
}

func decodeJSONRPC(t *testing.T, body []byte) (map[string]any, bool) {
	t.Helper()
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		return nil, true
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("jsonrpc: %v (%s)", err, body)
	}
	return req, false
}

func writeJSONRPC(w http.ResponseWriter, id any, result any) {
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
